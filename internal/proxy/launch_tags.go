package proxy

import (
	"encoding/base64"
	"net/http"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

var launchSession = regexp.MustCompile(`^[a-f0-9]{32}$`)

func cleanProject(s string) string {
	if len(s) > 255 {
		return ""
	}
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || r == '/' || r == '\\' {
			return -1
		}
		return r
	}, s)
}

func withLaunchTags(r *http.Request, path string) (*http.Request, string, bool) {
	parts := strings.SplitN(strings.TrimPrefix(path, "/tag/"), "/", 3)
	if len(parts) != 3 || len(parts[0]) > 344 || !launchSession.MatchString(parts[1]) {
		return r, path, false
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[0])
	project := string(raw)
	if err != nil || !utf8.ValidString(project) || project == "" || cleanProject(project) != project {
		return r, path, false
	}
	copy := r.Clone(r.Context())
	if copy.Header.Get(projectHeader) == "" {
		copy.Header.Set(projectHeader, project)
	}
	if copy.Header.Get(sessionHeader) == "" {
		copy.Header.Set(sessionHeader, "launch-"+parts[1])
	}
	return copy, "/" + parts[2], true
}
