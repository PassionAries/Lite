package themehttp

import (
	"encoding/json"
	"errors"
	"html"
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

var (
	ErrNoReleaseAssets = errors.New("GitHub release has no downloadable assets")

	releaseTagRe   = regexp.MustCompile(`/releases/tag/([^"'<\s?#]+)`)
	releaseAssetRe = regexp.MustCompile(`(?i)href="((?:https://github\.com)?/[^"]+/releases/download/[^"?#]+)"`)
)

type FetchFunc func(rawURL string, header http.Header) ([]byte, error)

func ResolveGitHubLatestRelease(owner, repo string, fetch FetchFunc) (tagName, downloadURL string, err error) {
	owner = strings.TrimSpace(owner)
	repo = strings.TrimSpace(repo)
	if owner == "" || repo == "" {
		return "", "", errors.New("GitHub owner and repository are required")
	}
	if fetch == nil {
		return "", "", ErrInvalidURL
	}

	latestURL := "https://github.com/" + url.PathEscape(owner) + "/" + url.PathEscape(repo) + "/releases/latest"
	body, fetchErr := fetch(latestURL, http.Header{"Accept": []string{"application/json"}})
	if fetchErr != nil {
		atomURL := "https://github.com/" + url.PathEscape(owner) + "/" + url.PathEscape(repo) + "/releases.atom"
		atom, atomErr := fetch(atomURL, nil)
		if atomErr != nil {
			return "", "", fetchErr
		}
		body = atom
	}

	tagName, err = ParseGitHubLatestTag(body)
	if err != nil {
		return "", "", err
	}
	if downloadURL, _ = ParseGitHubReleaseDownloadURL(body); downloadURL != "" {
		return tagName, downloadURL, nil
	}

	assetsURL := "https://github.com/" + url.PathEscape(owner) + "/" + url.PathEscape(repo) + "/releases/expanded_assets/" + url.PathEscape(tagName)
	assets, assetsErr := fetch(assetsURL, nil)
	if assetsErr != nil {
		return tagName, "", ErrNoReleaseAssets
	}
	downloadURL, err = ParseGitHubReleaseDownloadURL(assets)
	if err != nil {
		return tagName, "", ErrNoReleaseAssets
	}
	return tagName, downloadURL, nil
}

func ParseGitHubLatestTag(body []byte) (string, error) {
	trimmed := strings.TrimSpace(string(body))
	if trimmed == "" {
		return "", ErrEmpty
	}
	if strings.HasPrefix(trimmed, "{") {
		var payload struct {
			TagName string `json:"tag_name"`
			URL     string `json:"url"`
		}
		if err := json.Unmarshal([]byte(trimmed), &payload); err == nil {
			if tag := strings.TrimSpace(payload.TagName); tag != "" {
				return tag, nil
			}
			if tag := tagFromReleasePath(payload.URL); tag != "" {
				return tag, nil
			}
		}
	}
	if tag := tagFromReleasePath(trimmed); tag != "" {
		return tag, nil
	}
	return "", errors.New("unrecognized GitHub release response")
}

func ParseGitHubReleaseDownloadURL(body []byte) (string, error) {
	matches := releaseAssetRe.FindAllStringSubmatch(string(body), -1)
	var urls []string
	seen := make(map[string]struct{}, len(matches))
	for _, match := range matches {
		raw := html.UnescapeString(match[1])
		if strings.HasPrefix(raw, "/") {
			raw = "https://github.com" + raw
		}
		parsed, err := url.Parse(raw)
		if err != nil || !IsGitHubHost(parsed.Hostname()) {
			continue
		}
		if !strings.Contains(parsed.Path, "/releases/download/") {
			continue
		}
		canonical := parsed.Scheme + "://" + parsed.Host + parsed.EscapedPath()
		if _, ok := seen[canonical]; ok {
			continue
		}
		seen[canonical] = struct{}{}
		urls = append(urls, canonical)
	}
	picked := pickReleaseAsset(urls)
	if picked == "" {
		return "", ErrNoReleaseAssets
	}
	return picked, nil
}

func tagFromReleasePath(raw string) string {
	match := releaseTagRe.FindStringSubmatch(raw)
	if len(match) < 2 {
		return ""
	}
	return strings.TrimSuffix(html.UnescapeString(match[1]), "/")
}

func pickReleaseAsset(urls []string) string {
	var first string
	for _, raw := range urls {
		if first == "" {
			first = raw
		}
		if strings.HasSuffix(strings.ToLower(raw), ".zip") {
			return raw
		}
	}
	return first
}
