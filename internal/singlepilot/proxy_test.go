package singlepilot

import (
	"strings"
	"testing"
)

func TestStrictProxyOrigin(t *testing.T) {
	for _, raw := range []string{"http://localhost", "http://127.0.0.1:8080/", "https://edge.proxy.test:443", "http://[::1]:3128"} {
		u, err := strictProxyOrigin(raw)
		if err != nil || u.String() != strings.TrimSuffix(raw, "/") {
			t.Fatalf("valid origin %q", raw)
		}
	}
	for _, raw := range []string{"", " ", "http://", " http://p", "socks5://p", "http://p?", "https://p#", "https://p/x",
		"https://p:0", "http://p:", "https://p:65536", "https://p:+80", "http://a..b", "http://-p", "http://a_b",
		"http://[example]", "http://[127.0.0.1]", "http://::1", "https://[fe80::1%25lo0]", "http://user:secret@proxy",
		"https://@p", "https://p/%2f", "http://host\\xyz", "https://p/secret", "https://" + strings.Repeat("a", 2048)} {
		if u, err := strictProxyOrigin(raw); u != nil || err != errStrictProxy {
			t.Errorf("did not fail closed with a constant diagnostic")
		}
	}
}
