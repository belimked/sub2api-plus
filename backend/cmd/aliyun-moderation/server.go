package main

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"
)

// openAICategories are the OpenAI moderation categories sub2api-plus
// Content Moderation evaluates against its thresholds
// (backend/internal/service/content_moderation.go).
var openAICategories = []string{
	"harassment", "harassment/threatening", "hate", "hate/threatening",
	"illicit", "illicit/violent", "self-harm", "self-harm/intent",
	"self-harm/instructions", "sexual", "sexual/minors", "violence", "violence/graphic",
}

// mapLabel maps an Aliyun content label to the closest OpenAI category.
// Labels with no counterpart (politics, religion, contraband, ads, custom
// word lists, ...) become "illicit" so they still cross sub2api thresholds.
func mapLabel(label string) string {
	l := strings.ToLower(label)
	switch {
	case strings.Contains(l, "minor") || strings.Contains(l, "child"):
		return "sexual/minors"
	case strings.Contains(l, "porn") || strings.Contains(l, "sexual") || strings.Contains(l, "sex_"):
		return "sexual"
	case strings.Contains(l, "extremis") || strings.Contains(l, "terror"):
		return "illicit/violent"
	case strings.Contains(l, "violen") || strings.Contains(l, "weapon") || strings.Contains(l, "bloody"):
		return "violence"
	case strings.Contains(l, "self_harm") || strings.Contains(l, "suicide"):
		return "self-harm"
	case strings.Contains(l, "discriminat") || strings.Contains(l, "hate"):
		return "hate"
	case strings.Contains(l, "profanity") || strings.Contains(l, "abuse") || strings.Contains(l, "insult") || strings.Contains(l, "inappropriate_oral"):
		return "harassment"
	default:
		return "illicit"
	}
}

type moderationRequest struct {
	Model string          `json:"model"`
	Input json.RawMessage `json:"input"`
}

type moderationResult struct {
	Flagged        bool               `json:"flagged"`
	Categories     map[string]bool    `json:"categories"`
	CategoryScores map[string]float64 `json:"category_scores"`
}

type moderationResponse struct {
	ID      string             `json:"id"`
	Model   string             `json:"model"`
	Results []moderationResult `json:"results"`
}

type checker interface {
	check(ctx context.Context, content string) (*greenData, error)
}

type server struct {
	cfg    adapterConfig
	green  checker
	logger *slog.Logger
}

func (s *server) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("POST /v1/moderations", s.handleModeration)
	return mux
}

func (s *server) authorized(r *http.Request) bool {
	token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	return ok && subtle.ConstantTimeCompare([]byte(strings.TrimSpace(token)), []byte(s.cfg.APIKey)) == 1
}

func (s *server) handleModeration(w http.ResponseWriter, r *http.Request) {
	if !s.authorized(r) {
		writeError(w, http.StatusUnauthorized, "invalid api key")
		return
	}
	var req moderationRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 8<<20)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json body")
		return
	}
	text, err := inputText(req.Input)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	start := time.Now()
	result := moderationResult{Categories: map[string]bool{}, CategoryScores: map[string]float64{}}
	for _, c := range openAICategories {
		result.Categories[c] = false
		result.CategoryScores[c] = 0
	}
	chunks := splitChunks(text, s.cfg.ChunkChars, s.cfg.MaxChunks)
	datas, err := s.checkChunks(r.Context(), chunks)
	if err != nil {
		s.logger.Warn("aliyun-moderation: check failed", "error", err, "chunks", len(chunks), "chars", len([]rune(text)))
		writeError(w, http.StatusBadGateway, "aliyun moderation unavailable")
		return
	}
	var labels []string
	for _, d := range datas {
		labels = append(labels, s.apply(&result, d)...)
	}
	s.logger.Info("aliyun-moderation: checked",
		"flagged", result.Flagged, "labels", labels, "chunks", len(chunks),
		"chars", len([]rune(text)), "latency_ms", time.Since(start).Milliseconds())

	model := req.Model
	if model == "" {
		model = s.cfg.Service
	}
	writeJSON(w, http.StatusOK, moderationResponse{ID: "modr-" + nonce(), Model: model, Results: []moderationResult{result}})
}

// apply folds one Aliyun verdict into result. Hits at or above the
// configured level set their mapped OpenAI category to 1.0; every hit is
// also exposed as an "aliyun/<label>" score (confidence/100), which sub2api
// records but never evaluates against a threshold.
func (s *server) apply(result *moderationResult, d *greenData) []string {
	var labels []string
	contentHit := parseRiskLevel(d.RiskLevel) >= s.cfg.ContentMinLevel
	for _, item := range d.Result {
		if item.Label == "" || item.Label == "nonLabel" {
			continue
		}
		labels = append(labels, item.Label)
		setMax(result.CategoryScores, "aliyun/"+item.Label, item.Confidence/100)
		if contentHit {
			flag(result, mapLabel(item.Label))
		}
	}
	if s.cfg.AttackMinLevel != levelOff {
		for _, item := range d.AttackResult {
			if item.Label == "" || item.Label == "nonLabel" {
				continue
			}
			labels = append(labels, "attack:"+item.Label)
			setMax(result.CategoryScores, "aliyun/attack/"+item.Label, item.Confidence/100)
			if parseRiskLevel(item.AttackLevel) >= s.cfg.AttackMinLevel {
				flag(result, "illicit")
			}
		}
	}
	return labels
}

func flag(result *moderationResult, category string) {
	result.Flagged = true
	result.Categories[category] = true
	result.CategoryScores[category] = 1
}

func setMax(scores map[string]float64, key string, v float64) {
	if v > scores[key] {
		scores[key] = v
	}
}

func (s *server) checkChunks(ctx context.Context, chunks []string) ([]*greenData, error) {
	out := make([]*greenData, len(chunks))
	errs := make([]error, len(chunks))
	var wg sync.WaitGroup
	for i, chunk := range chunks {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out[i], errs[i] = s.green.check(ctx, chunk)
		}()
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

// inputText flattens an OpenAI moderation input (string, string array, or
// multimodal part array) to its text; image parts are ignored.
func inputText(raw json.RawMessage) (string, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return "", nil
	}
	var single string
	if err := json.Unmarshal(raw, &single); err == nil {
		return strings.TrimSpace(single), nil
	}
	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		return "", errInvalidInput
	}
	var parts []string
	for _, item := range items {
		var s string
		if err := json.Unmarshal(item, &s); err == nil {
			parts = append(parts, s)
			continue
		}
		var p struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if err := json.Unmarshal(item, &p); err != nil {
			return "", errInvalidInput
		}
		if p.Type == "text" {
			parts = append(parts, p.Text)
		}
	}
	return strings.TrimSpace(strings.Join(parts, "\n")), nil
}

type inputError string

func (e inputError) Error() string { return string(e) }

const errInvalidInput = inputError("input must be a string, string array or content part array")

// splitChunks cuts text into at most maxChunks pieces of size runes; text
// beyond that is not sent to Aliyun, bounding cost per request.
func splitChunks(text string, size, maxChunks int) []string {
	runes := []rune(text)
	if len(runes) == 0 {
		return nil
	}
	var chunks []string
	for len(runes) > 0 && len(chunks) < maxChunks {
		n := min(size, len(runes))
		chunks = append(chunks, string(runes[:n]))
		runes = runes[n:]
	}
	return chunks
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]any{"error": map[string]string{"message": msg}})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
