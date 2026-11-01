package auth

import "testing"

func TestIsLocalOrigin(t *testing.T) {
	cases := map[string]bool{
		"http://localhost:8080":           true,
		"http://binarysust.localhost":     true,
		"http://API.Binarysust.LOCALHOST": true,
		"http://127.0.0.1:3000":           true,
		"http://[::1]:3000":               true,
		"https://example.com":             false,
		"http://localhost.example.com":    false,
		"http://127.0.0.2":                false,
		"http://notlocalhost":             false,
		"localhost":                       false, // not an origin
		"":                                false,
	}
	for origin, want := range cases {
		if got := IsLocalOrigin(origin); got != want {
			t.Errorf("IsLocalOrigin(%q) = %v, want %v", origin, got, want)
		}
	}
}
