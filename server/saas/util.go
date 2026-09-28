package saas

import (
	"net/url"
	"strconv"
	"strings"
)

func itoa(i int) string { return strconv.Itoa(i) }

func encodeFlash(s string) string { return url.QueryEscape(s) }

func splitFlash(v string) (string, string) {
	i := strings.IndexByte(v, ':')
	if i < 0 {
		return "", ""
	}

	text, err := url.QueryUnescape(v[i+1:])
	if err != nil {
		return "", ""
	}

	return v[:i], text
}

// localPath returns the path+query of a (possibly absolute) URL, or fallback
// when the URL points elsewhere
func localPath(u, host, fallback string) string {
	p, err := url.Parse(u)
	if err != nil || (p.Host != "" && p.Host != host) || !strings.HasPrefix(p.Path, "/") {
		return fallback
	}

	if p.RawQuery != "" {
		return p.Path + "?" + p.RawQuery
	}

	return p.Path
}
