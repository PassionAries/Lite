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

// githubProxyPrefix returns the gh-proxy origin when u is already a
// rewritten GitHub URL (proxy + "/" + https://github...). Empty if not.
func githubProxyPrefix(u *url.URL) string {
	if u == nil || IsGitHubHost(u.Hostname()) {
		return ""
	}
	path := u.EscapedPath()
	if path == "" {
		path = u.Path
	}
	idx := strings.Index(path, "/https://")
	if idx < 0 {
		idx = strings.Index(path, "/http://")
	}
	if idx < 0 {
		return ""
	}
	inner, err := url.Parse(path[idx+1:])
	if err != nil || !IsGitHubHost(inner.Hostname()) {
		return ""
	}
	origin := u.Scheme + "://" + u.Host + path[:idx]
	return strings.TrimRight(origin, "/")
}
