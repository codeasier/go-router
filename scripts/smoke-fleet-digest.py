#!/usr/bin/env python3
"""Offline real-process test: two isolated gateways and one Dashboard."""
import contextlib
import hashlib
import json
from pathlib import Path
import re
import signal
import socket
import subprocess
import sys
import tempfile
import time
import urllib.error
import urllib.request

gateway, dashboard = map(lambda x: str(Path(x).resolve(strict=True)), sys.argv[1:3])
opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))

def addresses(count):
    with contextlib.ExitStack() as stack:
        listeners = [stack.enter_context(socket.socket()) for _ in range(count)]
        for listener in listeners:
            listener.bind(("127.0.0.1", 0))
        return [f"127.0.0.1:{s.getsockname()[1]}" for s in listeners]

def bytes_for(rev, model):
    return json.dumps(dict(schema_version=1, revision=rev, providers=[
        dict(id="synthetic", transport="chat", models=[dict(id=model)])
    ]), separators=(",", ":")).encode()

def get(addr, path="/", headers=None):
    with opener.open(urllib.request.Request(f"http://{addr}{path}", headers=headers or {}), timeout=1) as resp:
        return resp.read().decode()

def wait(proc, request, accepts):
    deadline = time.monotonic() + 12
    last = None
    while time.monotonic() < deadline:
        assert proc.poll() is None, f"Test process exited with {proc.returncode}"
        try:
            last = request()
            if accepts(last):
                return last
        except (urllib.error.URLError, TimeoutError, ConnectionError):
            pass
        time.sleep(0.15)
    raise AssertionError(f"Did not reach expected state: {last!r}")

def terminate(proc):
    if proc.poll() is None:
        proc.terminate()
        try:
            proc.wait(5)
        except subprocess.TimeoutExpired:
            proc.kill()
            proc.wait(3)

def rows(html):
    result = {}
    for node, data in re.findall(r'<tr>\s*<th scope="row">(.*?)</th>(.*?)</tr>', html, re.S):
        result[node] = re.findall(r"<td>(.*?)</td>", data, re.S)
    return result

def check_rows(page, a, b):
    result = rows(page)
    return sorted(result) == ["pilot-a", "pilot-b"] and any(a == cell for cell in result["pilot-a"]) and any(b == cell for cell in result["pilot-b"])

with tempfile.TemporaryDirectory(prefix="go-router-accept-") as directory, contextlib.ExitStack() as stack:
    path = Path(directory)
    a, am, b, bm, dash = addresses(5)
    keys = ("fixture-auth-a-01234567890123456789012345", "fixture-auth-b-01234567890123456789012345")
    expected = bytes_for("revision-one", "alpha")
    altered = bytes_for("revision-one", "bravo")
    dg = hashlib.sha256(expected).hexdigest()
    bdg = hashlib.sha256(altered).hexdigest()
    files = [path / "a.json", path / "b.json"]
    for file, body in zip(files, (expected, altered)):
        file.write_bytes(body)

    def launch(cmd, **env):
        process = subprocess.Popen(cmd, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, env={**env})
        stack.callback(terminate, process)
        return process

    def start_node(n):
        addr1, addr2 = [(a, am), (b, bm)][n]
        return launch([gateway, "-addr", addr1, "-admin-addr", addr2,
                       "-node-id", ["pilot-a", "pilot-b"][n],
                       "-config", str(files[n])], GO_ROUTER_MANAGEMENT_KEY=keys[n])

    def status(addr, n):
        return json.loads(get(addr, "/admin/v1/status", {"Authorization": "Bearer " + keys[n]}))

    p_a, p_b = start_node(0), start_node(1)
    for n, (proc, addr, digest) in enumerate([(p_a, am, dg), (p_b, bm, bdg)]):
        wait(proc, lambda: status(addr, n), lambda s: s.get("applied_revision") == "revision-one"
             and s.get("applied_digest") == digest)
    inv = path / "fleet.json"
    inv.touch(mode=0o600)
    inv.write_text(json.dumps({"nodes": [
        {"node_id": node, "admin_addr": addr, "bearer_env": env,
         "desired_revision": "revision-one", "desired_digest": dg}
        for node, addr, env in (("pilot-a", am, "GO_ROUTER_DASHBOARD_KEY_A"),
                                ("pilot-b", bm, "GO_ROUTER_DASHBOARD_KEY_B"))
    ]}))
    p_dash = launch([dashboard, "-addr", dash, "-config", str(inv)],
                    GO_ROUTER_DASHBOARD_KEY_A=keys[0], GO_ROUTER_DASHBOARD_KEY_B=keys[1])
    first = wait(p_dash, lambda: get(dash), lambda page: check_rows(page, "Matched", "Drift"))
    assert not any(key in first for key in keys)
    print("PASS: same revision label, different bytes; only the expected digest matches")
    terminate(p_b)
    files[1].write_bytes(expected)
    p_b = start_node(1)
    wait(p_dash, lambda: get(dash), lambda page: check_rows(page, "Matched", "Matched"))
    print("PASS: both independent nodes converge when configuration bytes match")

    replacement = path / "next.json"
    replacement.write_bytes(b"{")
    replacement.replace(files[0])
    p_a.send_signal(signal.SIGHUP)
    # Observe eventual stability after allowing the reload loop to process the
    # malformed candidate; the next valid reload supplies positive proof.
    time.sleep(0.35)
    assert status(am, 0)["applied_digest"] == dg
    revised = bytes_for("revision-two", "alpha")
    replacement.write_bytes(revised)
    replacement.replace(files[0])
    p_a.send_signal(signal.SIGHUP)
    wait(p_a, lambda: status(am, 0), lambda s:
         s.get("applied_revision") == "revision-two"
         and s.get("applied_digest") == hashlib.sha256(revised).hexdigest())
    wait(p_dash, lambda: get(dash), lambda page: check_rows(page, "Drift", "Matched"))
    assert status(bm, 1)["applied_digest"] == dg
    print("PASS: invalid reload retains digest; valid reload changes revision and digest only on A")
