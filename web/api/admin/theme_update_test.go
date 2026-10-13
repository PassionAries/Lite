package admin

import (
	"testing"
	"time"

	"github.com/nuomiiiii/lite/pkg/config"
	"github.com/stretchr/testify/require"
)

func TestThemeVersionsEqual(t *testing.T) {
	cases := []struct {
		installed string
		latest    string
		want      bool
	}{
		{"1.2.8", "v1.2.8", true},
		{"V1.2.8", "1.2.8", true},
		{" 1.2.8 ", "v1.2.8", true},
		{"1.2.8", "1.2.9", false},
		{"", "1.0.0", false},
		{"1.0.0", "", false},
		{"1.2.8-beta", "v1.2.8-beta", true},
	}
	for _, tc := range cases {
		if got := themeVersionsEqual(tc.installed, tc.latest); got != tc.want {
			t.Fatalf("themeVersionsEqual(%q, %q) = %t, want %t", tc.installed, tc.latest, got, tc.want)
		}
	}
}

func TestNormalizeThemeAutoUpdateHours(t *testing.T) {
	hours, err := normalizeThemeAutoUpdateHours(24)
	require.NoError(t, err)
	require.Equal(t, 24, hours)

	_, err = normalizeThemeAutoUpdateHours(0)
	require.Error(t, err)
	_, err = normalizeThemeAutoUpdateHours(config.ThemeAutoUpdateIntervalMaxHours + 1)
	require.Error(t, err)
	hours, err = normalizeThemeAutoUpdateHours(config.ThemeAutoUpdateIntervalMaxHours)
	require.NoError(t, err)
	require.Equal(t, config.ThemeAutoUpdateIntervalMaxHours, hours)
}

func TestThemeAutoUpdateDelay(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	require.Equal(t, themeAutoUpdateIdleWait, themeAutoUpdateDelay(now, false, now.Add(time.Hour).Unix()))
	require.Equal(t, 2*time.Hour, themeAutoUpdateDelay(now, true, now.Add(2*time.Hour).Unix()))
	require.Equal(t, time.Duration(0), themeAutoUpdateDelay(now, true, now.Add(-time.Minute).Unix()))
	require.Equal(t, time.Duration(0), themeAutoUpdateDelay(now, true, 0))
}

func TestShouldResetThemeAutoUpdate(t *testing.T) {
	require.True(t, shouldResetThemeAutoUpdate(false, 24, true, 12))
	require.True(t, shouldResetThemeAutoUpdate(true, 24, true, 12))
	require.False(t, shouldResetThemeAutoUpdate(true, 12, true, 12))
	require.False(t, shouldResetThemeAutoUpdate(true, 12, false, 24))
}

func TestThemeAutoUpdateAuditEvent(t *testing.T) {
	key, params := themeAutoUpdateAuditEvent(false, 24, true, 12)
	require.Equal(t, "audit.theme_auto_update_on", key)
	require.Equal(t, "12", params["hours"])

	key, params = themeAutoUpdateAuditEvent(true, 12, false, 12)
	require.Equal(t, "audit.theme_auto_update_off", key)
	require.Nil(t, params)

	key, params = themeAutoUpdateAuditEvent(true, 12, true, 24)
	require.Equal(t, "audit.theme_auto_update_interval", key)
	require.Equal(t, "24", params["hours"])

	key, params = themeAutoUpdateAuditEvent(true, 12, true, 12)
	require.Equal(t, "", key)
	require.Nil(t, params)

	key, params = themeAutoUpdateAuditEvent(false, 24, false, 12)
	require.Equal(t, "", key)
	require.Nil(t, params)
}

func TestThemeUpdateAuditParams(t *testing.T) {
	params := themeUpdateAuditParams(ThemeUpdateSummary{Updated: 1, Current: 2, Skipped: 3, Failed: 4})
	require.Equal(t, "1", params["updated"])
	require.Equal(t, "2", params["current"])
	require.Equal(t, "3", params["skipped"])
	require.Equal(t, "4", params["failed"])
}

func TestUpdateAllSkipsThemeWithoutSource(t *testing.T) {
	t.Chdir(t.TempDir())
	writeUsableTheme(t, "plain", "Lite-theme.json", `{"name":"Plain","short":"plain","version":"1.0.0","configuration":{"type":"managed","data":[]}}`)

	summary, err := updateAllInstalledThemes()
	require.NoError(t, err)
	require.Equal(t, 0, summary.Updated)
	require.Equal(t, 0, summary.Failed)
	require.Equal(t, 1, summary.Skipped)
	require.Len(t, summary.Results, 1)
	require.Equal(t, "plain", summary.Results[0].Short)
	require.Equal(t, themeUpdateStatusSkipped, summary.Results[0].Status)
	require.Equal(t, "1.0.0", summary.Results[0].Version)
}

func TestUpdateAllRejectsOverlap(t *testing.T) {
	themeUpdateAllMu.Lock()
	themeUpdateAllBusy = true
	themeUpdateAllMu.Unlock()
	t.Cleanup(func() {
		themeUpdateAllMu.Lock()
		themeUpdateAllBusy = false
		themeUpdateAllMu.Unlock()
	})

	_, err := updateAllInstalledThemes()
	require.ErrorIs(t, err, errThemeUpdateBusy)
}
