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
