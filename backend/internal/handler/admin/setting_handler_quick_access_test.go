//go:build unit

package admin

import (
	"net/http"
	"testing"

	"github.com/LuckyKuang/sub2api-plus/internal/service"

	"github.com/stretchr/testify/require"
)

func TestUpdateSettingsQuickAccessURL(t *testing.T) {
	h, repo := newStepUpSwitchTestHandler(t, map[string]string{})
	rec := doUpdateSettings(t, h, map[string]any{"quick_access_url": "  https://gw.example.com/entry  "}, nil)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "https://gw.example.com/entry", repo.values[service.SettingKeyQuickAccessURL])

	rec = doUpdateSettings(t, h, map[string]any{"site_name": "Example"}, nil)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "https://gw.example.com/entry", repo.values[service.SettingKeyQuickAccessURL],
		"an omitted quick_access_url keeps the stored value")

	rec = doUpdateSettings(t, h, map[string]any{"quick_access_url": ""}, nil)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "", repo.values[service.SettingKeyQuickAccessURL])

	for _, invalid := range []string{"gw.example.com/entry", "javascript:alert(1)", "ftp://gw.example.com/entry", "https://gw.example.com/entry#x"} {
		h, _ := newStepUpSwitchTestHandler(t, map[string]string{})
		rec := doUpdateSettings(t, h, map[string]any{"quick_access_url": invalid}, nil)
		require.Equal(t, http.StatusBadRequest, rec.Code, invalid)
	}
}
