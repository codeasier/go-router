package singlepilot

import (
	"errors"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"
)

var errStrictProxy = errors.New("invalid generic Chat strict proxy configuration")

func chatTransport() *http.Transport {
	return &http.Transport{
		DisableKeepAlives: true, DisableCompression: true,
		TLSHandshakeTimeout: 5 * time.Second, ResponseHeaderTimeout: 10 * time.Second,
		MaxResponseHeaderBytes: 32 << 10,
	}
}

// NewWithStrictProxy selects an operator-configured, anonymous HTTP(S) CONNECT
// proxy. An empty/malformed proxy is an error, never permission to go direct.
// The generic adapter constructs only HTTPS requests to its fixed upstream.
func NewWithStrictProxy(provider string, names []string, baseURL, key string, log journal, raw string) (http.Handler, error) {
	proxy, err := strictProxyOrigin(raw)
	if err != nil {
		return nil, err
	}
	t := chatTransport()
	t.Proxy = http.ProxyURL(proxy)
	return routes(provider, names, baseURL, key, log, t, 30*time.Second)
}

func strictProxyOrigin(raw string) (*url.URL, error) {
	if len(raw) > 2048 || !(strings.HasPrefix(raw, "http://") || strings.HasPrefix(raw, "https://")) ||
		strings.ContainsAny(raw, "?#%@\\") {
		return nil, errStrictProxy
	}
	u, err := url.Parse(raw)
	if err != nil || u.User != nil || u.Host == "" || u.Opaque != "" || (u.Path != "" && u.Path != "/") ||
		strings.HasSuffix(u.Host, ":") {
		return nil, errStrictProxy
	}
	h := u.Hostname()
	if h == "" || len(h) > 253 {
		return nil, errStrictProxy
	}
	ip, ipErr := netip.ParseAddr(h)
	if strings.HasPrefix(u.Host, "[") {
		if ipErr != nil || !ip.Is6() || ip.Zone() != "" {
			return nil, errStrictProxy
		}
	} else if ipErr != nil {
		for _, part := range strings.Split(h, ".") {
			if len(part) == 0 || len(part) > 63 || part[0] == '-' || part[len(part)-1] == '-' {
				return nil, errStrictProxy
			}
			for _, c := range part {
				if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-') {
					return nil, errStrictProxy
				}
			}
		}
	} else if !ip.Is4() {
		return nil, errStrictProxy // IPv6 literals require brackets.
	}
	if port := u.Port(); port != "" {
		n, err := strconv.ParseUint(port, 10, 16)
		if err != nil || n == 0 {
			return nil, errStrictProxy
		}
	}
	u.Path = ""
	return u, nil
}
