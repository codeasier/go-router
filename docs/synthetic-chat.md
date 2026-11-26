# Exercise a real gateway process with a synthetic peer

Linux/macOS only. Use a scratch directory on a trusted local filesystem and a
free loopback port. Nothing here reaches a model provider or 9router. Do not
send real prompts, configure credentials, or expose this listener via a proxy.

```sh
go build -o /tmp/go-router ./cmd/go-router
pilot=$(mktemp -d)
mkdir -m 700 "$pilot/journal"
cat >"$pilot/node.json" <<'JSON'
{"schema_version":1,"revision":"pilot-1","providers":[{"id":"fixture","aliases":["fx"],"transport":"synthetic-http","models":[{"id":"demo/chat","aliases":["alias"]}]}]}
JSON
/tmp/go-router -init-synthetic-journal "$pilot/journal"
/tmp/go-router -addr 127.0.0.1:20128 -config "$pilot/node.json" -synthetic-journal "$pilot/journal"
```

In another terminal:

```sh
curl -fsS http://127.0.0.1:20128/v1/models
curl -isS http://127.0.0.1:20128/v1/chat/completions -H 'Content-Type: application/json' \
  -d '{"model":"fx/alias","messages":[{"role":"user","content":"fixture"}]}'
```

The directory lists only canonical `fixture/demo/chat`; the fixed peer returns
`model: "demo/chat"` and the synthetic message. Set `"stream":true` for SSE
ending in `[DONE]`. Each accepted request records a durable dispatch intent
before contacting the owned loopback peer. Match `X-Request-ID` with the
journal and the process's `synthetic dispatch` log. Completion/usage values
are synthetic observations, not durable settlement, billing or retry authority.

To disable the model, set `enabled:false` on it, change the revision to
`pilot-2`, replace the file atomically, and send `SIGHUP` to this process.
Both canonical and alias now return 404, the directory is empty, and readiness
is 503. Invalid replacements retain the prior generation. In-flight requests
finish against the snapshot they started with. Without `-synthetic-journal` a
configuration is still metadata only. In synthetic mode even a nonempty
directory is local **synthetic readiness**, not real-provider readiness.

The listener requires its literal loopback Host (no browser Origin header).
The peer binds an ephemeral numeric loopback port; there is deliberately no
provider URL, DNS, proxy, credential or automatic fallback. The journal is
opened, never created or repaired, during startup. Reuse the initialized
journal after restart; never initialize over an existing one. Journal capacity
or write failure rejects dispatch; intent does not establish execution.
