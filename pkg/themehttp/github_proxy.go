package themehttp

import (
	"net/url"
	"strings"
)

func IsGitHubHost(host string) bool {
	host = strings.ToLower(strings.TrimSpace(host))
	host = strings.TrimSuffix(host, ".")
	if host == "github.com" || strings.HasSuffix(host, ".github.com") {
		return true
	}
	return host == "githubusercontent.com" || strings.HasSuffix(host, ".githubusercontent.com")
}

func NormalizeGitHubProxy(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return "", ErrInvalidURL
	}
	if err := validateParsedURL(parsed); err != nil {
		return "", err
	}
	parsed.Fragment = ""
	parsed.RawQuery = ""
	parsed.Path = strings.TrimRight(parsed.Path, "/")
	return strings.TrimRight(parsed.String(), "/"), nil
}

func RewriteGitHubURL(rawURL, proxy string) string {
	proxy = strings.TrimRight(strings.TrimSpace(proxy), "/")
	rawURL = strings.TrimSpace(rawURL)
	if proxy == "" || rawURL == "" {
		return rawURL
	}
	parsed, err := url.Parse(rawURL)
	if err != nil || !IsGitHubHost(parsed.Hostname()) {
		return rawURL
	}
	if strings.HasPrefix(rawURL, proxy+"/") {
		return rawURL
	}
	return proxy + "/" + rawURL
}
