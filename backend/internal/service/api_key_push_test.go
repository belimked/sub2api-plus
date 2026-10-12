//go:build unit

package service

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

type pushSettingRepoStub struct {
	SettingRepository
	values map[string]string
}

func (r *pushSettingRepoStub) GetMultiple(_ context.Context, keys []string) (map[string]string, error) {
	out := map[string]string{}
	for _, k := range keys {
		if v, ok := r.values[k]; ok {
			out[k] = v
		}
	}
	return out, nil
}

type pushGroupRepoStub struct {
	GroupRepository
	group *Group
}

func (r *pushGroupRepoStub) GetByID(context.Context, int64) (*Group, error) {
	return r.group, nil
}

type pushAuditRepoStub struct {
	AuditLogRepository
	mu   sync.Mutex
	logs []*AuditLog
}

func (r *pushAuditRepoStub) BatchInsert(_ context.Context, logs []*AuditLog) (int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.logs = append(r.logs, logs...)
	return int64(len(logs)), nil
}

func runAPIKeyPush(t *testing.T, settings map[string]string, key *APIKey) []*AuditLog {
	t.Helper()
	auditRepo := &pushAuditRepoStub{}
	audit := NewAuditLogService(auditRepo, nil)
	audit.Start()
	svc := NewAPIKeyPushService(
		NewSettingService(&pushSettingRepoStub{values: settings}, nil),
		&pushGroupRepoStub{group: &Group{ID: 7, Name: "cc"}},
		nil,
		audit,
	)
	svc.push(context.Background(), key)
	audit.Stop()
	return auditRepo.logs
}

func TestAPIKeyPushDeliversAndAudits(t *testing.T) {
	var got apiKeyPushPayload
	var gotToken, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotToken = r.Header.Get("X-Sub2api-Token")
		require.NoError(t, json.NewDecoder(r.Body).Decode(&got))
		_, _ = w.Write([]byte(`{"ok":true,"written":false}`))
	}))
	defer srv.Close()

	groupID := int64(7)
	logs := runAPIKeyPush(t, map[string]string{
		SettingKeyQuickAccessURL:        srv.URL + "/entry?x=1",
		SettingKeyQuickAccessPushSecret: "shared-secret",
	}, &APIKey{ID: 3, UserID: 15, GroupID: &groupID, Key: "sk-abcdef1234567890"})

	require.Equal(t, "/api/gw/tokens", gotPath)
	require.Equal(t, "shared-secret", gotToken)
	require.Equal(t, apiKeyPushPayload{UserID: "15", Group: "cc", APIKey: "sk-abcdef1234567890"}, got)

	require.Len(t, logs, 1)
	entry := logs[0]
	require.Equal(t, AuditActionAPIKeyPush, entry.Action)
	require.Equal(t, http.StatusOK, entry.StatusCode)
	require.Equal(t, int64(15), *entry.ActorUserID)
	require.Equal(t, "success", entry.Extra["result"])
	require.Equal(t, false, entry.Extra["written"])
	require.Equal(t, "sk-abc****7890", entry.CredentialMasked)
	encoded, _ := json.Marshal(entry)
	require.NotContains(t, string(encoded), "sk-abcdef1234567890")
	require.NotContains(t, string(encoded), "shared-secret")
}

func TestAPIKeyPushRecordsGatewayError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"该用户未绑定实例,拒绝推送"}`))
	}))
	defer srv.Close()

	groupID := int64(7)
	logs := runAPIKeyPush(t, map[string]string{
		SettingKeyQuickAccessURL:        srv.URL,
		SettingKeyQuickAccessPushSecret: "s",
	}, &APIKey{ID: 3, UserID: 15, GroupID: &groupID, Key: "sk-abcdef1234567890"})

	require.Len(t, logs, 1)
	require.Equal(t, http.StatusNotFound, logs[0].StatusCode)
	require.Equal(t, "failed", logs[0].Extra["result"])
	require.Equal(t, "该用户未绑定实例,拒绝推送", logs[0].Extra["error"])
}

func TestAPIKeyPushSkips(t *testing.T) {
	require.Empty(t, runAPIKeyPush(t, map[string]string{SettingKeyQuickAccessURL: "https://gw.example.com"},
		&APIKey{ID: 1, UserID: 1, Key: "sk-abcdef1234567890"}), "no secret configured: push disabled")

	logs := runAPIKeyPush(t, map[string]string{
		SettingKeyQuickAccessURL:        "https://gw.example.com",
		SettingKeyQuickAccessPushSecret: "s",
	}, &APIKey{ID: 1, UserID: 1, Key: "sk-abcdef1234567890"})
	require.Len(t, logs, 1)
	require.Equal(t, "skipped", logs[0].Extra["result"])
	require.Equal(t, "no_group", logs[0].Extra["reason"])
}
