package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

type fakeChecker struct {
	data  *greenData
	err   error
	calls []string
}

func (f *fakeChecker) check(_ context.Context, content string) (*greenData, error) {
	f.calls = append(f.calls, content)
	return f.data, f.err
}

func testServer(green checker) *server {
	return &server{
		cfg: adapterConfig{
			APIKey: "secret", Service: "query_security_check_pro",
			ContentMinLevel: levelHigh, AttackMinLevel: levelHigh, ChunkChars: 2000, MaxChunks: 3,
		},
		green:  green,
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

func post(t *testing.T, s *server, token, body string) (*httptest.ResponseRecorder, moderationResponse) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/moderations", strings.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	s.routes().ServeHTTP(rec, req)
	var out moderationResponse
	if rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("decode: %v", err)
		}
	}
	return rec, out
}

func TestModerationRequiresAPIKey(t *testing.T) {
	s := testServer(&fakeChecker{data: &greenData{RiskLevel: "none"}})
	for _, token := range []string{"", "wrong"} {
		rec, _ := post(t, s, token, `{"input":"hi"}`)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("token %q: status %d, want 401", token, rec.Code)
		}
	}
}

func TestModerationCleanInput(t *testing.T) {
	fake := &fakeChecker{data: &greenData{RiskLevel: "none", AttackLevel: "none",
		Result: []greenLabel{{Label: "nonLabel"}}, AttackResult: []greenLabel{{Label: "nonLabel", AttackLevel: "none"}}}}
	rec, out := post(t, testServer(fake), "secret", `{"model":"omni-moderation-latest","input":"写一个快速排序"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	r := out.Results[0]
	if r.Flagged || len(r.CategoryScores) != len(openAICategories) {
		t.Fatalf("clean input: %+v", r)
	}
	if out.Model != "omni-moderation-latest" || len(fake.calls) != 1 || fake.calls[0] != "写一个快速排序" {
		t.Fatalf("model %q calls %q", out.Model, fake.calls)
	}
}

func TestModerationHighContentRiskFlagsMappedCategory(t *testing.T) {
	fake := &fakeChecker{data: &greenData{RiskLevel: "high",
		Result: []greenLabel{{Label: "violent_weapons", Confidence: 95}}}}
	_, out := post(t, testServer(fake), "secret", `{"input":"..."}`)
	r := out.Results[0]
	if !r.Flagged || !r.Categories["violence"] || r.CategoryScores["violence"] != 1 {
		t.Fatalf("want violence flagged: %+v", r)
	}
	if r.CategoryScores["aliyun/violent_weapons"] != 0.95 {
		t.Fatalf("raw label score missing: %+v", r.CategoryScores)
	}
}

func TestModerationBelowMinLevelOnlyRecordsLabel(t *testing.T) {
	fake := &fakeChecker{data: &greenData{RiskLevel: "medium",
		Result: []greenLabel{{Label: "political_entity", Confidence: 60}}}}
	_, out := post(t, testServer(fake), "secret", `{"input":"..."}`)
	r := out.Results[0]
	if r.Flagged || r.CategoryScores["illicit"] != 0 || r.CategoryScores["aliyun/political_entity"] != 0.6 {
		t.Fatalf("medium risk must not flag: %+v", r)
	}
}

func TestModerationPromptAttack(t *testing.T) {
	data := &greenData{RiskLevel: "none", AttackLevel: "high",
		AttackResult: []greenLabel{{Label: "DAN Jailbreak", Confidence: 70, AttackLevel: "high"}}}

	_, out := post(t, testServer(&fakeChecker{data: data}), "secret", `{"input":"..."}`)
	if r := out.Results[0]; !r.Flagged || !r.Categories["illicit"] || r.CategoryScores["aliyun/attack/DAN Jailbreak"] != 0.7 {
		t.Fatalf("attack must flag illicit: %+v", r)
	}

	off := testServer(&fakeChecker{data: data})
	off.cfg.AttackMinLevel = levelOff
	_, out = post(t, off, "secret", `{"input":"..."}`)
	if r := out.Results[0]; r.Flagged || len(r.CategoryScores) != len(openAICategories) {
		t.Fatalf("attack off must ignore attack results: %+v", r)
	}
}

func TestModerationAliyunFailureIsBadGateway(t *testing.T) {
	rec, _ := post(t, testServer(&fakeChecker{err: errors.New("boom")}), "secret", `{"input":"hi"}`)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status %d, want 502", rec.Code)
	}
}

func TestModerationEmptyInputSkipsAliyun(t *testing.T) {
	fake := &fakeChecker{}
	rec, out := post(t, testServer(fake), "secret", `{"input":[{"type":"image_url","image_url":{"url":"https://x/y.png"}}]}`)
	if rec.Code != http.StatusOK || out.Results[0].Flagged || len(fake.calls) != 0 {
		t.Fatalf("status %d flagged %v calls %d", rec.Code, out.Results[0].Flagged, len(fake.calls))
	}
}

func TestInputText(t *testing.T) {
	cases := map[string]string{
		`"  hello "`: "hello",
		`["a","b"]`:  "a\nb",
		`[{"type":"text","text":"a"},{"type":"image_url","image_url":{"url":"u"}},{"type":"text","text":"b"}]`: "a\nb",
		`null`: "",
	}
	for raw, want := range cases {
		got, err := inputText(json.RawMessage(raw))
		if err != nil || got != want {
			t.Fatalf("%s: got %q err %v, want %q", raw, got, err, want)
		}
	}
	if _, err := inputText(json.RawMessage(`{"a":1}`)); err == nil {
		t.Fatal("object input must be rejected")
	}
}

func TestSplitChunks(t *testing.T) {
	text := strings.Repeat("字", 4500)
	chunks := splitChunks(text, 2000, 3)
	if len(chunks) != 3 || len([]rune(chunks[0])) != 2000 || len([]rune(chunks[2])) != 500 {
		t.Fatalf("chunks %d", len(chunks))
	}
	if got := splitChunks(text, 2000, 2); len(got) != 2 {
		t.Fatalf("max chunks not honored: %d", len(got))
	}
	if splitChunks("", 2000, 3) != nil {
		t.Fatal("empty text must yield no chunks")
	}
}

func TestMapLabel(t *testing.T) {
	cases := map[string]string{
		"violent_weapons":              "violence",
		"violent_extremist":            "illicit/violent",
		"pornographic_adult":           "sexual",
		"sexual_minors":                "sexual/minors",
		"inappropriate_discrimination": "hate",
		"inappropriate_profanity":      "harassment",
		"political_entity":             "illicit",
		"contraband_drug":              "illicit",
		"customized":                   "illicit",
	}
	for label, want := range cases {
		if got := mapLabel(label); got != want {
			t.Fatalf("%s: got %s want %s", label, got, want)
		}
	}
}

func TestGreenClientSignedRequest(t *testing.T) {
	var form url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		form = r.PostForm
		_, _ = w.Write([]byte(`{"Code":200,"Message":"OK","Data":{"RiskLevel":"high","Result":[{"Label":"violent_weapons","Confidence":95}]}}`))
	}))
	defer srv.Close()

	c := newGreenClient(adapterConfig{Endpoint: srv.URL, Region: "cn-shanghai", Service: "query_security_check_pro",
		AccessKeyID: "ak", AccessKeySecret: "sk", Timeout: time.Second})
	c.now = func() time.Time { return time.Date(2026, 9, 29, 1, 2, 3, 0, time.UTC) }
	d, err := c.check(context.Background(), "内容")
	if err != nil || d.RiskLevel != "high" {
		t.Fatalf("check: %+v %v", d, err)
	}
	for k, want := range map[string]string{
		"Action": "TextModerationPlus", "Version": "2022-03-02", "AccessKeyId": "ak", "RegionId": "cn-shanghai",
		"Service": "query_security_check_pro", "ServiceParameters": `{"content":"内容"}`, "Timestamp": "2026-09-29T01:02:03Z",
	} {
		if form.Get(k) != want {
			t.Fatalf("%s = %q, want %q", k, form.Get(k), want)
		}
	}
	if form.Get("Signature") != rpcSignature(http.MethodPost, form, "sk") {
		t.Fatal("signature does not match canonical form")
	}
}

func TestGreenClientErrorCode(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"Code":403,"Message":"Forbidden.RAM"}`))
	}))
	defer srv.Close()
	c := newGreenClient(adapterConfig{Endpoint: srv.URL, AccessKeyID: "ak", AccessKeySecret: "sk", Timeout: time.Second})
	if _, err := c.check(context.Background(), "x"); err == nil || !strings.Contains(err.Error(), "Forbidden.RAM") {
		t.Fatalf("want Forbidden.RAM error, got %v", err)
	}
}

// percentEncode follows RFC 3986 as the Aliyun RPC signature requires.
func TestPercentEncode(t *testing.T) {
	if got := percentEncode("a b*c~d/"); got != "a%20b%2Ac~d%2F" {
		t.Fatalf("got %s", got)
	}
}
