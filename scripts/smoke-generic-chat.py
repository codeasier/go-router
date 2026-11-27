#!/usr/bin/env python3
"""Run the binary against a local, explicitly trusted HTTPS Chat fixture."""
import hashlib
import http.client
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import json
from pathlib import Path
import signal
import socket
import ssl
import subprocess
import sys
import tempfile
import threading
import time

binary = str(Path(sys.argv[1]).resolve(strict=True))
with tempfile.TemporaryDirectory(prefix="go-router-generic-") as tmp:
    root = Path(tmp)
    journal = root / "journal"
    journal.mkdir(mode=0o700)
    cert, key = root / "cert.pem", root / "key.pem"
    subprocess.run(
        ["openssl", "req", "-x509", "-newkey", "rsa:2048", "-nodes", "-days", "1",
         "-subj", "/CN=127.0.0.1", "-addext", "subjectAltName=IP:127.0.0.1",
         "-out", str(cert), "-keyout", str(key)],
        stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, check=True,
    )
    subprocess.run([binary, "-init-generic-chat-journal", str(journal)], check=True, env={})
    calls = []
    observed_modes = []
    canceled = threading.Event()

    class Fixture(BaseHTTPRequestHandler):
        def log_message(self, *_):
            pass

        def do_POST(self):
            assert self.path == "/v1/chat/completions"
            assert self.headers["Authorization"] == "Bearer fixture-upstream-key"
            assert self.headers.get("Cookie") is None
            assert self.headers["Accept-Encoding"] == "identity"
            raw = self.rfile.read(int(self.headers["Content-Length"]))
            payload = json.loads(raw)
            model = payload["model"]
            records = (journal / "dispatch-intents.v1").read_bytes().splitlines()
            assert records[0] == b"go-router/dispatch-intents/v1"
            digest, record = records[-1].split(b"\t", 1)
            assert hashlib.sha256(record).hexdigest().encode() == digest
            intent = json.loads(record)["intent"]
            assert intent["model"] == f"custom/{model}"
            assert bytes(intent["body_digest"]) == hashlib.sha256(raw).digest()
            calls.append(model)
            if model == "schema":
                assert payload["response_format"] == {"type": "json_object"}
                assert payload["messages"][0]["role"] == "system"
                assert '\n```json\n{\n  "type": "object"\n}\n```\n' in payload["messages"][0]["content"]
                assert payload["messages"][1] == {"role": "user", "content": "hello"}
            if model == "normalized":
                assert payload["messages"] == [
                    {"role": "system", "content": "rules"}, {"role": "user", "content": "hello"}]
                assert "tools" not in payload and "stream_options" not in payload
            if model == "redirect":
                self.send_response(307)
                self.send_header("Location", "/must-not-follow")
                self.end_headers()
                return
            if model == "limited":
                self.send_response(429)
                self.send_header("Content-Type", "application/json")
                self.send_header("Retry-After", "30")
                self.end_headers()
                self.wfile.write(b'{"error":"opaque-provider-detail"}')
                return
            accepts = self.headers.get_all("Accept")
            observed_modes.append((payload, accepts))
            assert accepts in (["text/event-stream"], ["*/*"]), accepts
            stream = accepts == ["text/event-stream"]
            if model == "json-only":
                stream = False  # A compatible upstream may use only the JSON body field.

            self.send_response(200)
            self.send_header("Content-Type", "text/event-stream" if stream else "application/json")
            self.end_headers()
            if stream:
                self.wfile.write(b'data: {"object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"fixture"}}]}\n\n')
                self.wfile.flush()
                if model == "cancel":
                    self.connection.settimeout(3)
                    try:
                        if self.rfile.read(1) == b"":
                            canceled.set()
                    except (OSError, TimeoutError):
                        pass
                    return
                if model != "interrupt":
                    self.wfile.write(b"data: [DONE]\n\n")
            else:
                self.wfile.write(b'{"object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"fixture"},"finish_reason":"stop"}]}')

    peer = ThreadingHTTPServer(("127.0.0.1", 0), Fixture)
    tls = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
    tls.load_cert_chain(cert, key)
    peer.socket = tls.wrap_socket(peer.socket, server_side=True)
    thread = threading.Thread(target=peer.serve_forever, daemon=True)
    thread.start()
    with socket.socket() as reserve:
        reserve.bind(("127.0.0.1", 0))
        port = reserve.getsockname()[1]

    args = [binary, "-addr", f"127.0.0.1:{port}", "-generic-chat-provider", "custom",
            "-generic-chat-base-url", f"https://127.0.0.1:{peer.server_port}/v1",
            "-generic-chat-models", "success,interrupt,redirect,cancel,limited,json-only,normalized,schema", "-generic-chat-journal", str(journal)]
    env = {"GO_ROUTER_GENERIC_CHAT_KEY": "fixture-upstream-key", "SSL_CERT_FILE": str(cert),
           "HTTPS_PROXY": "http://127.0.0.1:1"}
    bad = args.copy()
    bad[2] = f"0.0.0.0:{port}"
    assert subprocess.run(bad, env=env, timeout=2, stdout=subprocess.DEVNULL).returncode != 0

    def call(path, body=None, headers=None):
        conn = http.client.HTTPConnection("127.0.0.1", port, timeout=3)
        try:
            if body is None:
                conn.request("GET", path, headers=headers or {})
            else:
                conn.request("POST", path, json.dumps(body),
                             {"Content-Type": "application/json", "Cookie": "foreign",
                              "Authorization": "Bearer client-key", **(headers or {})})
            res = conn.getresponse()
            return res.status, res.getheader("Content-Type"), res.read()
        finally:
            conn.close()

    process = subprocess.Popen(args, env=env)
    try:
        deadline = time.monotonic() + 5
        while time.monotonic() < deadline:
            assert process.poll() is None
            try:
                if call("/readyz")[0] == 200:
                    break
            except (TimeoutError, ConnectionError):
                pass
            time.sleep(.05)
        else:
            raise AssertionError("gateway did not become ready")
        assert call("/healthz")[0] == 200
        assert call("/v1/models", headers={"Origin": "https://unrelated.invalid"})[0] == 403
        assert [m["id"] for m in json.loads(call("/v1/models")[2])["data"]] == [
            "custom/cancel", "custom/interrupt", "custom/json-only", "custom/limited", "custom/normalized", "custom/redirect", "custom/schema", "custom/success"]
        chat = "/v1/chat/completions"
        # Pinned 9router ordinary OpenAI client/non-force-stream generic provider.
        # Values are expectations, independently of the upstream fixture.
        accepts = [None, "*/*", "application/json", "text/event-stream",
                   "application/json, text/event-stream", "application/json; q=0"]
        matrix = [
            (None, [True, True, False, True, True, False]),
            (True, [True] * len(accepts)),
            (False, [False] * len(accepts)),
        ]
        for field, modes in matrix:
            for accept, wants_sse in zip(accepts, modes, strict=True):
                body_fields = {} if field is None else {"stream": field}
                headers = {} if accept is None else {"Accept": accept}
                code, media, body = call(chat, {"model": "custom/success", **body_fields}, headers)
                assert observed_modes[-1] == ({"model": "success", **body_fields},
                                              ["text/event-stream" if wants_sse else "*/*"])
                assert code == 200
                if wants_sse:
                    assert media == "text/event-stream" and body.endswith(b"data: [DONE]\n\n")
                else:
                    assert media == "application/json" and json.loads(body)["object"] == "chat.completion"

        # The wire still prefers SSE on omission. Without an explicit SSE
        # requirement the local pilot preserves an actual, validated JSON reply.
        code, media, body = call(chat, {"model": "custom/json-only"})
        assert observed_modes[-1] == ({"model": "json-only"}, ["text/event-stream"])
        assert code == 200 and media == "application/json" and json.loads(body)["object"] == "chat.completion"
        for body_fields, headers in [({"stream": True}, {}), ({}, {"Accept": "text/event-stream"})]:
            code, media, body = call(chat, {"model": "custom/json-only", **body_fields}, headers)
            assert code == 502 and media == "application/json"
            assert json.loads(body) == {"error": {"message": "invalid upstream response"}}
        code, _, body = call(chat, {"model": "custom/interrupt", "stream": True})
        assert code == 200 and b"fixture" in body and b"[DONE]" not in body
        assert call(chat, {"model": "custom/redirect"})[0] == 502
        for stream in [False, True]:
            code, media, body = call(chat, {"model": "custom/limited", "stream": stream})
            assert code == 429 and media == "application/json"
            assert json.loads(body) == {"error": {"message": "upstream rate limited"}}
        assert call(chat, {
            "model": "custom/normalized", "stream": False, "tools": [],
            "stream_options": {"include_usage": True},
            "messages": [{"role": "developer", "content": "rules"}, {"role": "user", "content": "hello"}],
        })[0] == 200
        assert call(chat, {
            "model": "custom/schema", "stream": False,
            "response_format": {"type": "json_schema", "json_schema": {"schema": {"type": "object"}}},
            "messages": [{"role": "user", "content": "hello"}],
        })[0] == 200
        assert call(chat, {"model": "custom/missing"})[0] == 404
        with socket.create_connection(("127.0.0.1", port), timeout=3) as conn:
            raw = b'{"model":"custom/cancel","stream":true}'
            conn.sendall(
                f"POST {chat} HTTP/1.1\r\nHost: 127.0.0.1:{port}\r\nContent-Type: application/json\r\n"
                f"Content-Length: {len(raw)}\r\n\r\n".encode() + raw
            )
            received = bytearray()
            while b"data:" not in received:
                part = conn.recv(4096)
                assert part, "gateway closed before first frame"
                received.extend(part)
            conn.shutdown(socket.SHUT_RDWR)
        assert canceled.wait(3), "upstream connection remained open after downstream cancel"
        assert calls == ["success"] * (len(accepts) * len(matrix)) + ["json-only"] * 3 + [
            "interrupt", "redirect", "limited", "limited", "normalized", "schema", "cancel"]
    finally:
        if process.poll() is None:
            process.send_signal(signal.SIGTERM)
            process.wait(timeout=12)
        peer.shutdown()
        peer.server_close()
    assert process.returncode == 0
    print("generic Chat: loopback-only, catalog, authenticated TLS, 18 Chat negotiation cases, implicit JSON fallback, journal-first, truncation, rate limit, cancellation and no redirect PASS")
