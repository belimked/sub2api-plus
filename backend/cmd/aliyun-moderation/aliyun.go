package main

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

type riskLevel int

const (
	levelOff riskLevel = iota - 1
	levelNone
	levelLow
	levelMedium
	levelHigh
)

func parseRiskLevel(s string) riskLevel {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "low":
		return levelLow
	case "medium":
		return levelMedium
	case "high":
		return levelHigh
	case "off":
		return levelOff
	default:
		return levelNone
	}
}

// greenLabel is one Result/AttackResult entry of a TextModerationPlus reply.
type greenLabel struct {
	Label       string  `json:"Label"`
	Description string  `json:"Description"`
	Confidence  float64 `json:"Confidence"`
	AttackLevel string  `json:"AttackLevel"`
}

type greenData struct {
	RiskLevel    string       `json:"RiskLevel"`
	AttackLevel  string       `json:"AttackLevel"`
	Result       []greenLabel `json:"Result"`
	AttackResult []greenLabel `json:"AttackResult"`
}

type greenResponse struct {
	Code    int        `json:"Code"`
	Message string     `json:"Message"`
	Data    *greenData `json:"Data"`
}

// greenClient calls the Aliyun AI Guardrails TextModerationPlus RPC API
// (Version 2022-03-02) signed with the RPC HMAC-SHA1 scheme.
type greenClient struct {
	endpoint        string
	region          string
	service         string
	accessKeyID     string
	accessKeySecret string
	http            *http.Client
	now             func() time.Time
}

func newGreenClient(cfg adapterConfig) *greenClient {
	return &greenClient{
		endpoint:        cfg.Endpoint,
		region:          cfg.Region,
		service:         cfg.Service,
		accessKeyID:     cfg.AccessKeyID,
		accessKeySecret: cfg.AccessKeySecret,
		http:            &http.Client{Timeout: cfg.Timeout},
		now:             time.Now,
	}
}

func (c *greenClient) check(ctx context.Context, content string) (*greenData, error) {
	params, err := json.Marshal(map[string]string{"content": content})
	if err != nil {
		return nil, err
	}
	form := url.Values{
		"Action":            {"TextModerationPlus"},
		"Version":           {"2022-03-02"},
		"Format":            {"JSON"},
		"AccessKeyId":       {c.accessKeyID},
		"SignatureMethod":   {"HMAC-SHA1"},
		"SignatureVersion":  {"1.0"},
		"SignatureNonce":    {nonce()},
		"Timestamp":         {c.now().UTC().Format("2006-01-02T15:04:05Z")},
		"RegionId":          {c.region},
		"Service":           {c.service},
		"ServiceParameters": {string(params)},
	}
	form.Set("Signature", rpcSignature(http.MethodPost, form, c.accessKeySecret))

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint+"/", strings.NewReader(form.Encode())) //nolint:gosec // G704: endpoint 只来自运维配置的 ALIYUN_GREEN_ENDPOINT/REGION，请求内容不参与 URL 构造
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := c.http.Do(req) //nolint:gosec // G704: 同上
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	var out greenResponse
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("aliyun status %d: undecodable body", resp.StatusCode)
	}
	if resp.StatusCode != http.StatusOK || out.Code != http.StatusOK || out.Data == nil {
		return nil, fmt.Errorf("aliyun status %d code %d: %s", resp.StatusCode, out.Code, out.Message)
	}
	return out.Data, nil
}

// rpcSignature implements the Aliyun RPC signature: HMAC-SHA1 over
// METHOD&%2F&percentEncode(canonicalized query), keyed with secret + "&".
func rpcSignature(method string, params url.Values, secret string) string {
	keys := make([]string, 0, len(params))
	for k := range params {
		if k != "Signature" {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	pairs := make([]string, 0, len(keys))
	for _, k := range keys {
		pairs = append(pairs, percentEncode(k)+"="+percentEncode(params.Get(k)))
	}
	stringToSign := method + "&" + percentEncode("/") + "&" + percentEncode(strings.Join(pairs, "&"))
	mac := hmac.New(sha1.New, []byte(secret+"&"))
	_, _ = mac.Write([]byte(stringToSign))
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

func percentEncode(s string) string {
	e := url.QueryEscape(s)
	e = strings.ReplaceAll(e, "+", "%20")
	e = strings.ReplaceAll(e, "*", "%2A")
	return strings.ReplaceAll(e, "%7E", "~")
}

func nonce() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
