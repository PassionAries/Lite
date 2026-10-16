package themehttp

import (
	"errors"
	"net/http"
	"net/url"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseGitHubLatestTag(t *testing.T) {
	tag, err := ParseGitHubLatestTag([]byte(`{"tag_name":"v1.2.8","url":"https://github.com/nuomiiiii/Lite-theme/releases/tag/v1.2.8"}`))
	require.NoError(t, err)
	require.Equal(t, "v1.2.8", tag)

	tag, err = ParseGitHubLatestTag([]byte(`<feed><entry><link href="https://github.com/nuomiiiii/Lite-theme/releases/tag/v1.2.9"/></entry></feed>`))
	require.NoError(t, err)
	require.Equal(t, "v1.2.9", tag)

	tag, err = ParseGitHubLatestTag([]byte(`<meta property="og:url" content="https://github.com/nuomiiiii/Lite-theme/releases/tag/v2.0.0">`))
	require.NoError(t, err)
	require.Equal(t, "v2.0.0", tag)

	_, err = ParseGitHubLatestTag([]byte(`{"message":"Not Found"}`))
	require.Error(t, err)
}

func TestParseGitHubReleaseDownloadURLPrefersZip(t *testing.T) {
	html := `
<a href="/nuomiiiii/Lite-theme/archive/refs/tags/v1.2.8.zip">Source</a>
<a href="/nuomiiiii/Lite-theme/releases/download/v1.2.8/notes.txt">notes</a>
<a href="/nuomiiiii/Lite-theme/releases/download/v1.2.8/Lite-theme.zip">zip</a>
`
	got, err := ParseGitHubReleaseDownloadURL([]byte(html))
	require.NoError(t, err)
	require.Equal(t, "https://github.com/nuomiiiii/Lite-theme/releases/download/v1.2.8/Lite-theme.zip", got)
}

func TestResolveGitHubLatestReleaseUsesGithubHostNotAPI(t *testing.T) {
	var urls []string
	tag, download, err := ResolveGitHubLatestRelease("nuomiiiii", "Lite-theme", func(rawURL string, header http.Header) ([]byte, error) {
		urls = append(urls, rawURL)
		switch {
		case rawURL == "https://github.com/nuomiiiii/Lite-theme/releases/latest":
			require.Equal(t, "application/json", header.Get("Accept"))
			return []byte(`{"tag_name":"v1.2.8"}`), nil
		case rawURL == "https://github.com/nuomiiiii/Lite-theme/releases/expanded_assets/v1.2.8":
			return []byte(`<a href="/nuomiiiii/Lite-theme/releases/download/v1.2.8/Lite-theme.zip">zip</a>`), nil
		default:
			t.Fatalf("unexpected url %s", rawURL)
			return nil, errors.New("unexpected")
		}
	})
	require.NoError(t, err)
	require.Equal(t, "v1.2.8", tag)
	require.Equal(t, "https://github.com/nuomiiiii/Lite-theme/releases/download/v1.2.8/Lite-theme.zip", download)
	require.Equal(t, []string{
		"https://github.com/nuomiiiii/Lite-theme/releases/latest",
		"https://github.com/nuomiiiii/Lite-theme/releases/expanded_assets/v1.2.8",
	}, urls)
	for _, raw := range urls {
		require.NotContains(t, raw, "api.github.com")
	}
}

func TestResolveGitHubLatestReleaseFallsBackToAtom(t *testing.T) {
	tag, download, err := ResolveGitHubLatestRelease("nuomiiiii", "Lite-theme", func(rawURL string, _ http.Header) ([]byte, error) {
		switch rawURL {
		case "https://github.com/nuomiiiii/Lite-theme/releases/latest":
			return nil, ErrHTTPStatus
		case "https://github.com/nuomiiiii/Lite-theme/releases.atom":
			return []byte(`<entry><link href="https://github.com/nuomiiiii/Lite-theme/releases/tag/v9.9.9"/></entry>`), nil
		case "https://github.com/nuomiiiii/Lite-theme/releases/expanded_assets/v9.9.9":
			return []byte(`<a href="https://github.com/nuomiiiii/Lite-theme/releases/download/v9.9.9/pack.zip">zip</a>`), nil
		default:
			t.Fatalf("unexpected url %s", rawURL)
			return nil, errors.New("unexpected")
		}
	})
	require.NoError(t, err)
	require.Equal(t, "v9.9.9", tag)
	require.Equal(t, "https://github.com/nuomiiiii/Lite-theme/releases/download/v9.9.9/pack.zip", download)
}

func TestGitHubProxyPrefix(t *testing.T) {
	parsed, err := url.Parse("https://gh-proxy.com/https://github.com/owner/repo/releases/latest")
	require.NoError(t, err)
	require.Equal(t, "https://gh-proxy.com", githubProxyPrefix(parsed))

	parsed, err = url.Parse("https://example.com/gh/https://raw.githubusercontent.com/owner/repo/main/file")
	require.NoError(t, err)
	require.Equal(t, "https://example.com/gh", githubProxyPrefix(parsed))

	parsed, err = url.Parse("https://github.com/owner/repo/releases/latest")
	require.NoError(t, err)
	require.Equal(t, "", githubProxyPrefix(parsed))
}
