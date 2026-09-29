//go:build unit || !integration

package federation

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// newStatusTestClient returns a client whose login succeeds and whose
// /target endpoint answers with the given status and raw body.
func newStatusTestClient(t *testing.T, status int, body string) *AdminAPIClient {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/auth/login", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]string{"access_token": "t"}})
	})
	mux.HandleFunc("/target", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return NewAdminAPIClient(server.URL, "admin@example.com", "pw", 5*time.Second)
}

func TestAuthedDo_NonJSONErrorBodyIsClassifiedByStatus(t *testing.T) {
	for _, tc := range []struct {
		name      string
		status    int
		body      string
		retryable bool
	}{
		{name: "plain-text 404 from a deployment missing the endpoint is terminal", status: http.StatusNotFound, body: "404 page not found", retryable: false},
		{name: "html 502 from a proxy stays retryable", status: http.StatusBadGateway, body: "<html>bad gateway</html>", retryable: true},
		{name: "plain-text 429 stays retryable", status: http.StatusTooManyRequests, body: "slow down", retryable: true},
		{name: "invalid JSON on 2xx stays retryable", status: http.StatusOK, body: "not json", retryable: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := newStatusTestClient(t, tc.status, tc.body)
			_, err := c.AuthedDo(context.Background(), http.MethodPost, "/target", []byte(`{}`), "k")
			require.Error(t, err)
			require.Equal(t, tc.retryable, IsRetryable(err), "err: %v", err)
		})
	}
}
