# Single-instance generic Chat pilot

This is a loopback-only pilot for one operator-configured OpenAI-compatible **Chat** connection. It exposes the configured models, supports explicit JSON and SSE Chat, and does not need the Dashboard. It is off by default. It is not 9router parity: this slice does not implement native VolceAPI, Responses, Messages, proxy configuration, fallback, custom headers, or usage settlement.

Use a trusted local host. Loopback and exact Host checks are not user authentication; do not publish this pilot through a reverse proxy. The destination is one operator-approved HTTPS origin and path with normal TLS verification. The inlet cannot choose the upstream. The pilot does not use ambient proxy settings, follow redirects, or replay an attempt.

```sh
mkdir -m 700 "$HOME/go-router-chat-journal"
go-router -init-generic-chat-journal "$HOME/go-router-chat-journal"
# Set GO_ROUTER_GENERIC_CHAT_KEY via the local secret environment, not in CLI arguments.
go-router -addr 127.0.0.1:20128 -generic-chat-provider custom \
  -generic-chat-base-url https://provider.example/v1 -generic-chat-models example-model \
  -generic-chat-journal "$HOME/go-router-chat-journal"
```

The base path is used as written, with `/chat/completions` appended; include `/v1` if required. Do not put a credential in the URL or model IDs. The inbound model ID is `custom/example-model` and the upstream model ID is `example-model`. This mode is separate from synthetic, reloadable node configuration, and the management listener. `/readyz` reports local readiness, not an upstream credential or availability probe.

The dispatch journal is required: its intent is persisted before forwarding a request. A recorded intent without a complete application result does not prove successful upstream execution. A failed or interrupted stream is not automatically retried. A full journal refuses new dispatch; this pilot does not rotate or delete records.

## Offline acceptance

```sh
go build -o /tmp/go-router ./cmd/go-router
python3 scripts/smoke-generic-chat.py /tmp/go-router
```

The test uses a private temporary journal, a local TLS fixture, and dummy credentials. It compares the actual sent body to the journal digest and verifies JSON, SSE, truncation and redirect rejection against a built binary. It does not send traffic to a real supplier.
