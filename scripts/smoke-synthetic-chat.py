#!/usr/bin/env python3
"""Rehearse only a self-contained go-router synthetic endpoint."""
import hashlib
import http.client
import json
from pathlib import Path
import signal
import socket
import subprocess
import sys
import tempfile
import time

binary = str(Path(sys.argv[1]).resolve(strict=True))
with tempfile.TemporaryDirectory(prefix="go-router-synthetic-") as tmp:
    root = Path(tmp)
    journal_dir = root / "journal"
    journal_dir.mkdir(mode=0o700)
    config = root / "node.json"
    subprocess.run([binary, "-init-synthetic-journal", str(journal_dir)], check=True, env={})
    journal = journal_dir / "dispatch-intents.v1"

    def write(revision, enabled):
        value = dict(schema_version=1, revision=revision, providers=[
            dict(id="fixture", aliases=["fx"], transport="synthetic-http",
                 models=[dict(id="demo/chat", aliases=["alias"], enabled=enabled)])
        ])
        alternate = root / "next.json"
        alternate.write_text(json.dumps(value))
        alternate.replace(config)

    with socket.socket() as server:
        server.bind(("127.0.0.1", 0))
        port = server.getsockname()[1]

    def request(path, body=None):
        conn = http.client.HTTPConnection("127.0.0.1", port, timeout=2)
        try:
            if body is None:
                conn.request("GET", path)
            else:
                conn.request("POST", path, json.dumps(body), {"Content-Type": "application/json"})
            response = conn.getresponse()
            return response.status, response.headers, response.read()
        finally:
            conn.close()

    def await_result(check):
        deadline = time.monotonic() + 10
        while time.monotonic() < deadline:
            assert process.poll() is None, f"gateway exited with {process.returncode}"
            try:
                if check():
                    return
            except (TimeoutError, ConnectionError):
                pass
            time.sleep(0.1)
        raise AssertionError("did not reach expected condition")

    def list_models():
        code, _, body = request("/v1/models")
        assert code == 200
        return [m["id"] for m in json.loads(body)["data"]]

    def intents():
        header, *lines = journal.read_bytes().splitlines()
        assert header == b"go-router/dispatch-intents/v1"
        records = []
        for line in lines:
            sha, record = line.split(b"\t", 1)
            assert hashlib.sha256(record).hexdigest().encode() == sha
            records.append(json.loads(record)["intent"])
        return records

    def chat(model, stream=False):
        return request("/v1/chat/completions", {
            "model": model, "stream": stream, "messages": [{"role": "user", "content": "fixture message"}],
        })

    write("revision-one", True)
    process = subprocess.Popen([binary, "-addr", f"127.0.0.1:{port}", "-config",
                                str(config), "-synthetic-journal", str(journal_dir)], env={})
    try:
        await_result(lambda: list_models() == ["fixture/demo/chat"])
        assert request("/readyz")[0] == 200
        ids = []
        for model, stream in [("fixture/demo/chat", False), ("fx/alias", True)]:
            code, headers, body = chat(model, stream)
            assert code == 200, (code, body)
            ids.append(headers["X-Request-ID"])
            if stream:
                assert b"data: [DONE]" in body and b"demo/chat" in body
            else:
                result = json.loads(body)
                assert result["model"] == "demo/chat" and result["choices"]
        assert ids == [rec["request_id"] for rec in intents()]
        assert b"fixture message" not in journal.read_bytes()
        print("PASS: canonical listing, JSON canonical request, SSE alias request, two corresponding journal intents")

        write("revision-two", False)
        process.send_signal(signal.SIGHUP)
        await_result(lambda: list_models() == [])
        assert request("/readyz")[0] == 503
        assert [chat(name)[0] for name in ("fixture/demo/chat", "fx/alias")] == [404, 404]
        assert len(intents()) == 2
        print("PASS: reload removes the disabled model and alias without sending additional requests")
    finally:
        if process.poll() is None:
            process.terminate()
            try:
                process.wait(timeout=5)
            except subprocess.TimeoutExpired:
                process.kill()
                process.wait(timeout=3)
