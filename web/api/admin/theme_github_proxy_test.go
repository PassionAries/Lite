package admin

import (
	"testing"

	"github.com/nuomiiiii/lite/pkg/themehttp"
	"github.com/stretchr/testify/require"
)

func TestApplyThemeGitHubProxyUsesGeneralProxy(t *testing.T) {
	raw := "https://github.com/nuomiiiii/Lite-theme/releases/latest/download/theme.zip"
	require.Equal(t, raw, applyThemeGitHubProxy(raw))
	require.Equal(t, raw, themehttp.RewriteGitHubURL(raw, ""))
	require.Equal(
		t,
		"https://gh-proxy.com/"+raw,
		themehttp.RewriteGitHubURL(raw, "https://gh-proxy.com"),
	)
}
