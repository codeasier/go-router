package singlepilot

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"
)

var errStrictProxy = errors.New("invalid generic Chat strict proxy configuration")

const strictProxySetupTimeout = 10 * time.Second
const strictProxyMaxConnections = 16

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
	return routes(provider, names, baseURL, key, log, newStrictProxyTransport(proxy, strictProxySetupTimeout), 30*time.Second)
}

type strictProxyTransport struct {
	base    *http.Transport
	timeout time.Duration
}

func newStrictProxyTransport(origin *url.URL, timeout time.Duration) strictProxyTransport {
	t := chatTransport()
	t.Proxy = http.ProxyURL(origin)
	t.DialContext = dialStrictProxy
	// A tunnel belongs to a single request; never let a multiplexed pool reuse
	// an in-flight connection that another request may cancel.
	t.Protocols = new(http.Protocols)
	t.Protocols.SetHTTP1(true)
	// Bound live and establishing connections independently of handler slots.
	// A canceled request can release its handler slot before DialContext exits.
	t.MaxConnsPerHost = strictProxyMaxConnections
	return strictProxyTransport{base: t, timeout: timeout}
}

type strictProxySetupKey struct{}

type strictProxySetup struct {
	caller   context.Context
	deadline time.Time
}

func (t strictProxyTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	caller, cancel := context.WithCancelCause(req.Context())
	deadline := time.Now().Add(t.timeout)
	if d, ok := caller.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	// Cancel even if the transport has not begun dialing (all slots busy).
	// A socket deadline also prevents a delayed timer from allowing more I/O.
	timer := time.AfterFunc(time.Until(deadline), func() { cancel(context.DeadlineExceeded) })
	defer timer.Stop()
	// net/http preserves context values but intentionally detaches cancellation
	// while dialing (including CONNECT). Retain the caller for this isolated
	// connection; let the standard library parse CONNECT and verify both TLS peers.
	ctx := context.WithValue(caller, strictProxySetupKey{}, strictProxySetup{caller: caller, deadline: deadline})
	ctx = httptrace.WithClientTrace(ctx, &httptrace.ClientTrace{GotConn: func(info httptrace.GotConnInfo) {
		if !timer.Stop() {
			cancel(context.DeadlineExceeded)
		}
		finishStrictProxySetup(caller, info)
	}})
	response, err := t.base.RoundTrip(req.WithContext(ctx))
	if err != nil {
		cancel(nil)
		return nil, err
	}
	// Keep cancellation connected to the original caller while the response
	// body is read. The setup timer has already stopped at handoff.
	response.Body = &strictProxyResponseBody{ReadCloser: response.Body, cancel: cancel}
	return response, nil
}

type strictProxyResponseBody struct {
	io.ReadCloser
	cancel context.CancelCauseFunc
}

func (body *strictProxyResponseBody) Close() error {
	defer body.cancel(nil)
	return body.ReadCloser.Close()
}

type strictProxySocket struct {
	net.Conn
	deadline time.Time
	stop     func() bool
}

func (c *strictProxySocket) Close() error {
	c.stop()
	return c.Conn.Close()
}

func dialStrictProxy(ctx context.Context, network, addr string) (net.Conn, error) {
	setup, ok := ctx.Value(strictProxySetupKey{}).(strictProxySetup)
	if !ok {
		return nil, errStrictProxy
	}
	dialCtx, cancel := context.WithDeadline(setup.caller, setup.deadline)
	defer cancel()
	c, err := (&net.Dialer{}).DialContext(dialCtx, network, addr)
	if err != nil {
		return nil, err
	}
	// One absolute bound covers proxy TLS, CONNECT and origin TLS, not a
	// fresh allowance for each stage. Closing the raw TCP socket interrupts
	// a pending read/write in either TLS layer without waiting for close_notify.
	if err := c.SetDeadline(setup.deadline); err != nil {
		_ = c.Close()
		return nil, err
	}
	stop := context.AfterFunc(setup.caller, func() { _ = c.Close() })
	return &strictProxySocket{Conn: c, deadline: setup.deadline, stop: stop}, nil
}

func finishStrictProxySetup(caller context.Context, info httptrace.GotConnInfo) {
	wire := info.Conn
	for range 2 { // Origin TLS, then optional HTTPS proxy TLS.
		tlsConn, ok := wire.(*tls.Conn)
		if !ok {
			break
		}
		wire = tlsConn.NetConn()
	}
	c, ok := wire.(*strictProxySocket)
	if !ok || info.Reused {
		_ = wire.Close()
		return
	}
	if !c.stop() || caller.Err() != nil {
		_ = c.Close()
		return
	}
	// The outer request still owns cancellation/timeouts after handoff. If the
	// setup deadline already passed, leave it enforced (I/O fails as a timeout).
	if time.Now().Before(c.deadline) {
		if err := c.SetDeadline(time.Time{}); err != nil {
			_ = c.Close()
		}
	}
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
