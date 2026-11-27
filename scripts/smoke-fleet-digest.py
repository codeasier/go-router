#!/usr/bin/env python3
"""Offline real-process test: two isolated gateways and one Dashboard."""
import contextlib
import hashlib
import http.client
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

def bytes_for(rev, *models):
    return json.dumps(dict(schema_version=1, revision=rev, providers=[
        dict(id="synthetic", transport="synthetic-http", models=[dict(id=model) for model in models])
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
    journals = [path / "journal-a", path / "journal-b"]
    logs = [path / "node-a.log", path / "node-b.log"]
    for journal in journals:
        journal.mkdir(mode=0o700)
        subprocess.run([gateway, "-init-synthetic-journal", str(journal)], check=True, env={})

    def launch(cmd, *, stdout=subprocess.DEVNULL, **env):
        process = subprocess.Popen(cmd, stdout=stdout, stderr=subprocess.DEVNULL, env={**env})
        stack.callback(terminate, process)
        return process

    def start_node(n, *, synthetic=True):
        addr1, addr2 = [(a, am), (b, bm)][n]
        args = [gateway, "-addr", addr1, "-admin-addr", addr2,
                "-node-id", ["pilot-a", "pilot-b"][n], "-config", str(files[n])]
        if synthetic:
            args += ["-synthetic-journal", str(journals[n])]
        with logs[n].open("ab") as log:
            return launch(args, stdout=log, GO_ROUTER_MANAGEMENT_KEY=keys[n])

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
    # The same independently pinned candidate drives an offline preview and
    # both live checks. None of these commands may rewrite the inventory or
    # the candidate. The preflight preview must run with no bearer variables.
    candidate_path = path / "candidate.json"
    candidate_path.write_bytes(expected)
    original_inventory = inv.read_bytes()
    preflight_args = [dashboard, "preflight", "-config", str(inv), "-candidate",
                      str(candidate_path), "-node", "pilot-b", "-node", "pilot-a"]

    def preflight(check, code, want):
        env = ({"GO_ROUTER_DASHBOARD_KEY_A": keys[0],
                "GO_ROUTER_DASHBOARD_KEY_B": keys[1]} if check else {})
        result = subprocess.run(preflight_args + (["-check"] if check else []),
                                env=env, timeout=5, capture_output=True, text=True)
        assert result.returncode == code and not result.stderr, "preflight exit or diagnostic"
        assert not any(value in result.stdout for value in (*keys, "GO_ROUTER_DASHBOARD_KEY_", am, bm))
        report = json.loads(result.stdout)
        assert report["schema_version"] == 1
        assert report["target"] == {"desired_revision": "revision-one", "desired_digest": dg}
        assert [row["node_id"] for row in report["nodes"]] == ["pilot-a", "pilot-b"]
        assert [row["comparison"] for row in report["nodes"]] == want
        assert report["converged"] is (all(state == "Matched" for state in want) if check else None)
        assert candidate_path.read_bytes() == expected and inv.read_bytes() == original_inventory

    preflight(False, 0, ["Unverified", "Unverified"])
    preflight(True, 1, ["Matched", "Drift"])
    print("PASS: offline plan without credentials and read-only per-node drift receipts")
    p_dash = launch([dashboard, "-addr", dash, "-config", str(inv)],
                    GO_ROUTER_DASHBOARD_KEY_A=keys[0], GO_ROUTER_DASHBOARD_KEY_B=keys[1])
    first = wait(p_dash, lambda: get(dash), lambda page: check_rows(page, "Matched", "Drift"))
    assert not any(key in first for key in keys)
    print("PASS: same revision label, different bytes; only the expected digest matches")
    terminate(p_b)
    files[1].write_bytes(expected)
    p_b = start_node(1, synthetic=False)
    both = wait(p_dash, lambda: get(dash), lambda page: check_rows(page, "Matched", "Matched"))
    assert "Ready" in rows(both)["pilot-a"] and "Not ready" in rows(both)["pilot-b"]
    preflight(True, 0, ["Matched", "Matched"])
    assert status(am, 0)["ready"] and not status(bm, 1)["ready"]
    print("PASS: identical bytes can converge while one node is still not ready")
    terminate(p_b)
    p_b = start_node(1)
    wait(p_b, lambda: status(bm, 1), lambda s: s["ready"])

    replacement = path / "next.json"
    before_rejection = logs[0].stat().st_size
    replacement.write_bytes(b"{")
    replacement.replace(files[0])
    p_a.send_signal(signal.SIGHUP)
    wait(p_a, lambda: logs[0].read_bytes()[before_rejection:],
         lambda log: b"node configuration reload rejected" in log)
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


    # Rehearse the intended controller contract with operator-owned local
    # publication, not a Dashboard write or an ingress policy change.
    def reload_node(proc, node, payload):
        staged = files[node].with_suffix(".next")
        staged.write_bytes(payload)
        staged.replace(files[node])
        proc.send_signal(signal.SIGHUP)

    def wait_revision(proc, admin, node, revision, payload):
        digest = hashlib.sha256(payload).hexdigest()
        return wait(proc, lambda: status(admin, node), lambda s: s == {
            "node_id": ["pilot-a", "pilot-b"][node], "ready": True,
            "applied_revision": revision, "applied_digest": digest,
        })

    def observe_candidate(revision, payload, expected, expected_ready):
        path_candidate = path / f"{revision}.json"
        path_candidate.write_bytes(payload)
        args = [dashboard, "preflight", "-config", str(inv), "-candidate",
                str(path_candidate), "-node", "pilot-a", "-node", "pilot-b", "-check"]
        result = subprocess.run(args, env={
            "GO_ROUTER_DASHBOARD_KEY_A": keys[0], "GO_ROUTER_DASHBOARD_KEY_B": keys[1],
        }, timeout=5, capture_output=True, text=True)
        assert not result.stderr and result.returncode == (0 if expected == ["Matched"] * 2 else 1)
        report = json.loads(result.stdout)
        assert report["target"] == {"desired_revision": revision,
                                    "desired_digest": hashlib.sha256(payload).hexdigest()}
        assert [row["comparison"] for row in report["nodes"]] == expected
        assert [row["observation"] == "Ready" for row in report["nodes"]] == expected_ready
        assert report["converged"] is (expected == ["Matched"] * 2)
        assert inv.read_bytes() == original_inventory and path_candidate.read_bytes() == payload

    def model_list(addr):
        return [row["id"] for row in json.loads(get(addr, "/v1/models"))["data"]]

    def check_chat(addr, model, expected_status):
        host, port = addr.rsplit(":", 1)
        with contextlib.closing(http.client.HTTPConnection(host, int(port), timeout=2)) as conn:
            conn.request("POST", "/v1/chat/completions", json.dumps({
                "model": "synthetic/" + model, "stream": True,
                "messages": [{"role": "user", "content": "isolated fleet fixture"}],
            }), {"Content-Type": "application/json"})
            response = conn.getresponse()
            body = response.read()
            assert response.status == expected_status
            if expected_status == 200:
                assert f'"model":"{model}"'.encode() in body and b"data: [DONE]" in body
                return response.getheader("X-Request-ID")
            return None

    release = bytes_for("pilot-add", "alpha", "charlie")
    reload_node(p_a, 0, release)
    wait_revision(p_a, am, 0, "pilot-add", release)
    before_rejection = logs[1].stat().st_size
    reload_node(p_b, 1, b"{")
    wait(p_b, lambda: logs[1].read_bytes()[before_rejection:],
         lambda log: b"node configuration reload rejected" in log)
    wait_revision(p_b, bm, 1, "revision-one", expected)
    observe_candidate("pilot-add", release, ["Matched", "Drift"], [True, True])
    assert model_list(a) == ["synthetic/alpha", "synthetic/charlie"] and model_list(b) == ["synthetic/alpha"]
    id_a = check_chat(a, "charlie", 200)
    assert check_chat(b, "charlie", 404) is None
    id_b = check_chat(b, "alpha", 200)
    print("PASS: partial apply is not convergence; rejected node continues serving last accepted model")

    # Retry exactly the planned bytes only after B positively rejected the bad
    # file and while A already matches. Successful apply is independent proof.
    reload_node(p_b, 1, release)
    wait_revision(p_b, bm, 1, "pilot-add", release)
    observe_candidate("pilot-add", release, ["Matched", "Matched"], [True, True])
    assert model_list(a) == model_list(b) == ["synthetic/alpha", "synthetic/charlie"]

    # A stopped controller does not take down the data planes.
    terminate(p_dash)
    assert model_list(a) == model_list(b) == ["synthetic/alpha", "synthetic/charlie"]

    rollback = bytes_for("pilot-rollback", "alpha")
    for proc, admin, node in ((p_a, am, 0), (p_b, bm, 1)):
        reload_node(proc, node, rollback)
        wait_revision(proc, admin, node, "pilot-rollback", rollback)
    observe_candidate("pilot-rollback", rollback, ["Matched", "Matched"], [True, True])
    assert model_list(a) == model_list(b) == ["synthetic/alpha"]
    assert check_chat(a, "charlie", 404) is None
    assert check_chat(b, "charlie", 404) is None
    assert id_a and id_b
    for journal, request_id in zip(journals, (id_a, id_b)):
        log = (journal / "dispatch-intents.v1").read_bytes()
        assert len(log.splitlines()) == 2 and request_id.encode() in log
        assert b"isolated fleet fixture" not in log
    print("PASS: retry converges, controller loss is independent, fresh rollback restores both models")
