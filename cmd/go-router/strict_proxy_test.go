package main

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/pem"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// Test only a fixed local tunnel: never dial an address supplied by a client.
func TestGenericStrictProxyProcess(t *testing.T) {
	dir := t.TempDir()
	binary := filepath.Join(dir, "router")
	build := exec.Command("go", "build", "-buildvcs=false", "-o", binary, ".")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v %s", err, out)
	}

	var upstream, tunnels, ambient atomic.Int32
	cancelled := make(chan struct{}, 1)
	origin := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstream.Add(1)
		if r.Method != "POST" || r.URL.Path != "/v1/chat/completions" ||
			r.Header.Get("Authorization") != "Bearer fixture-key" || r.Header.Get("Proxy-Authorization") != "" ||
			r.Header.Get("Cookie") != "" {
			t.Error("invalid upstream method/path or credential isolation")
			w.WriteHeader(400)
			return
		}
		b, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
			return
		}
		if bytes.Contains(b, []byte(`"stream":true`)) {
			w.Header().Set("Content-Type", "text/event-stream")
			io.WriteString(w, "data: {\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"}}]}\n\n")
			w.(http.Flusher).Flush()
			select {
			case <-r.Context().Done():
				cancelled <- struct{}{}
			case <-time.After(4 * time.Second):
			}
			return
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`)
	}))
	defer origin.Close()
	target, err := url.Parse(origin.URL)
	if err != nil {
		t.Fatal(err)
	}
	bad := atomic.Bool{}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tunnels.Add(1)
		if r.Method != "CONNECT" || r.Host != target.Host || r.Header.Get("Proxy-Authorization") != "" ||
			r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
			t.Error("invalid CONNECT method/target or leaked credential")
			w.WriteHeader(400)
			return
		}
		if bad.Load() {
			http.Error(w, "sensitive-proxy-error", http.StatusProxyAuthRequired)
			return
		}
		remote, err := net.DialTimeout("tcp", target.Host, 2*time.Second)
		if err != nil {
			t.Error(err)
			return
		}
		defer remote.Close()
		conn, rw, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.Close()
		conn.SetDeadline(time.Now().Add(8 * time.Second))
		remote.SetDeadline(time.Now().Add(8 * time.Second))
		if _, err = rw.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
			return
		}
		if err = rw.Flush(); err != nil {
			return
		}
		done := make(chan struct{})
		go func() { io.Copy(remote, rw); remote.Close(); close(done) }()
		io.Copy(conn, remote)
		conn.Close()
		<-done
	})
	proxy := httptest.NewServer(handler)
	defer proxy.Close()
	tlsProxy := httptest.NewTLSServer(handler)
	defer tlsProxy.Close()
	trap := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { ambient.Add(1); w.WriteHeader(503) }))
	defer trap.Close()

	ca := filepath.Join(dir, "ca.pem")
	var trust []byte
	for _, c := range []*x509.Certificate{origin.Certificate(), tlsProxy.Certificate()} {
		trust = append(trust, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: c.Raw})...)
	}
	if err := os.WriteFile(ca, trust, 0600); err != nil {
		t.Fatal(err)
	}
	emptyTrustDir := t.TempDir()
	emptyTrust := filepath.Join(emptyTrustDir, "no-certs.pem")
	if err := os.WriteFile(emptyTrust, nil, 0600); err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Transport: &http.Transport{Proxy: nil, DisableKeepAlives: true}, Timeout: 5 * time.Second}
	run := func(t *testing.T, proxyURL, trustFile string, expectInvalid bool, use func(string)) {
		t.Helper()
		temp := t.TempDir()
		journal := filepath.Join(temp, "journal")
		if err := os.Mkdir(journal, 0700); err != nil {
			t.Fatal(err)
		}
		init := exec.Command(binary, "-init-generic-chat-journal", journal)
		if out, err := init.CombinedOutput(); err != nil {
			t.Fatalf("journal: %v %s", err, out)
		}
		l, err := net.Listen("tcp4", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		addr := l.Addr().String()
		l.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, binary, "-addr", addr, "-generic-chat-provider", "custom", "-generic-chat-base-url", origin.URL+"/v1", "-generic-chat-models", "one", "-generic-chat-journal", journal)
		cmd.Env = []string{"GO_ROUTER_GENERIC_CHAT_KEY=fixture-key", "GO_ROUTER_GENERIC_CHAT_STRICT_PROXY_URL=" + proxyURL, "SSL_CERT_FILE=" + trustFile, "SSL_CERT_DIR=" + emptyTrustDir, "HTTPS_PROXY=" + trap.URL, "HTTP_PROXY=" + trap.URL, "NO_PROXY=*"}
		var logs bytes.Buffer
		cmd.Stdout = &logs
		cmd.Stderr = &logs
		if expectInvalid {
			if err := cmd.Run(); err == nil || ctx.Err() != nil || !strings.Contains(logs.String(), errConfigMessage) || strings.Contains(logs.String(), "sentinel-secret") {
				t.Errorf("invalid configuration did not fail safely")
			}
			return
		}
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		defer func() { cmd.Process.Kill(); cmd.Wait() }()
		gateway := "http://" + addr
		deadline := time.Now().Add(3 * time.Second)
		for {
			r, err := client.Get(gateway + "/readyz")
			if err == nil {
				r.Body.Close()
				if r.StatusCode == 200 {
					break
				}
			}
			if time.Now().After(deadline) {
				t.Fatal("loopback gateway failed to become ready")
			}
			time.Sleep(20 * time.Millisecond)
		}
		use(gateway)
	}
	post := func(t *testing.T, gateway string, stream bool) *http.Response {
		t.Helper()
		req, err := http.NewRequest("POST", gateway+"/v1/chat/completions", strings.NewReader(`{"model":"custom/one","stream":`+strconv.FormatBool(stream)+`}`))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Proxy-Authorization", "Basic untrusted")
		req.Header.Set("Authorization", "Bearer inbound")
		req.Header.Set("Cookie", "foreign=1")
		r, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	for _, v := range []struct{ name, p string }{{"HTTP", proxy.URL}, {"HTTPS", tlsProxy.URL}} {
		t.Run(v.name, func(t *testing.T) {
			beforeU, beforeT := upstream.Load(), tunnels.Load()
			run(t, v.p, ca, false, func(gateway string) {
				r := post(t, gateway, false)
				body, err := io.ReadAll(r.Body)
				r.Body.Close()
				if err != nil || r.StatusCode != 200 || !bytes.Contains(body, []byte(`"ok"`)) {
					t.Fatal("fixed configured tunnel did not deliver Chat")
				}
				r = post(t, gateway, true)
				chunk := make([]byte, 32)
				n, err := r.Body.Read(chunk)
				r.Body.Close()
				if err != nil || n == 0 || r.StatusCode != 200 {
					t.Fatal("missing SSE frame")
				}
				select {
				case <-cancelled:
				case <-time.After(3 * time.Second):
					t.Fatal("downstream disconnect did not cancel tunneled upstream")
				}
				bad.Store(true)
				r = post(t, gateway, false)
				body, _ = io.ReadAll(r.Body)
				r.Body.Close()
				bad.Store(false)
				if r.StatusCode != 502 || bytes.Contains(body, []byte("sensitive-proxy-error")) {
					t.Fatal("proxy rejection was not sanitized or fell back")
				}
			})
			if upstream.Load() != beforeU+2 || tunnels.Load() != beforeT+3 || ambient.Load() != 0 {
				t.Fatal("unexpected replay, proxy bypass or direct fallback")
			}
		})
	}
	for _, v := range []struct {
		name, proxy  string
		connectCount int32
	}{
		{"reject-untrusted-upstream", proxy.URL, 1},
		{"reject-untrusted-proxy", tlsProxy.URL, 0},
	} {
		t.Run(v.name, func(t *testing.T) {
			u, n := upstream.Load(), tunnels.Load()
			run(t, v.proxy, emptyTrust, false, func(gateway string) {
				r := post(t, gateway, false)
				defer r.Body.Close()
				if r.StatusCode != 502 {
					t.Fatal("untrusted TLS peer accepted")
				}
			})
			if upstream.Load() != u || tunnels.Load() != n+v.connectCount || ambient.Load() != 0 {
				t.Fatal("unexpected fallback or connection")
			}
		})
	}
	t.Run("invalid", func(t *testing.T) {
		u, n := upstream.Load(), tunnels.Load()
		for _, raw := range []string{"", "https://user:sentinel-secret@127.0.0.1", "socks5://127.0.0.1"} {
			run(t, raw, ca, true, nil)
		}
		if upstream.Load() != u || tunnels.Load() != n || ambient.Load() != 0 {
			t.Fatal("invalid configuration made a request")
		}
	})
}

const errConfigMessage = "invalid generic Chat strict proxy configuration"
