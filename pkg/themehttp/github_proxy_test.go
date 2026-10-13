package themehttp

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestIsGitHubHost(t *testing.T) {
	require.True(t, IsGitHubHost("github.com"))
	require.True(t, IsGitHubHost("API.GitHub.com"))
	require.True(t, IsGitHubHost("raw.githubusercontent.com"))
	require.True(t, IsGitHubHost("objects.githubusercontent.com"))
	require.False(t, IsGitHubHost("ghfast.top"))
	require.False(t, IsGitHubHost("example.com"))
	require.False(t, IsGitHubHost("notgithub.com"))
}

func TestNormalizeGitHubProxy(t *testing.T) {
	empty, err := NormalizeGitHubProxy("  ")
	require.NoError(t, err)
	require.Equal(t, "", empty)

	normalized, err := NormalizeGitHubProxy("https://ghfast.top/")
	require.NoError(t, err)
	require.Equal(t, "https://ghfast.top", normalized)

	normalized, err = NormalizeGitHubProxy("ghfast.top")
	require.NoError(t, err)
	require.Equal(t, "https://ghfast.top", normalized)

	_, err = NormalizeGitHubProxy("ftp://ghfast.top")
	require.ErrorIs(t, err, ErrUnsupportedScheme)

	_, err = NormalizeGitHubProxy("http://127.0.0.1:8080")
	require.ErrorIs(t, err, ErrPrivateAddress)
}

func TestRewriteGitHubURL(t *testing.T) {
	proxy := "https://ghfast.top"
	github := "https://github.com/owner/repo/releases/latest/download/theme.zip"
	api := "https://api.github.com/repos/owner/repo/releases/latest"
	raw := "https://raw.githubusercontent.com/owner/repo/main/v1.json"
	other := "https://cdn.example.com/theme.zip"

	require.Equal(t, github, RewriteGitHubURL(github, ""))
	require.Equal(t, proxy+"/"+github, RewriteGitHubURL(github, proxy+"/"))
	require.Equal(t, proxy+"/"+api, RewriteGitHubURL(api, proxy))
	require.Equal(t, proxy+"/"+raw, RewriteGitHubURL(raw, proxy))
	require.Equal(t, other, RewriteGitHubURL(other, proxy))
	require.Equal(t, proxy+"/"+github, RewriteGitHubURL(proxy+"/"+github, proxy))
}
