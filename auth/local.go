package auth

import (
	"net/url"
	"strings"
)

// IsLocalOrigin reports whether origin (e.g. "http://binarysust.localhost") is a local
// development origin: host localhost, 127.0.0.1, ::1, or a name ending in ".localhost".
// ekman drops the Secure flag on its cookies only for such origins, because local proxies
// often serve plain HTTP; every other origin gets Secure cookies. Apps can use it to gate
// dev-only sign-in on the same rule.
func IsLocalOrigin(origin string) bool {
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" {
		return false
	}
	host := strings.ToLower(u.Hostname())
	switch host {
	case "localhost", "127.0.0.1", "::1":
		return true
	}
	return strings.HasSuffix(host, ".localhost")
}
