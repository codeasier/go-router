# Single-instance generic Chat pilot

This is a loopback-only pilot for one operator-configured OpenAI-compatible **Chat** connection. It exposes the configured models, supports explicit JSON and SSE Chat, and does not need the Dashboard. It is off by default. It is not 9router parity: this slice does not implement native VolceAPI, Responses, Messages, proxy pools, fallback, or usage settlement.

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

The dispatch journal is required: its intent is persisted before forwarding a request. A recorded intent without a complete application result does not prove successful upstream execution. A failed or interrupted stream is not automatically retried. An upstream 429 returns a sanitized JSON 429 before any stream begins, without forwarding the provider error body or retry headers. A full journal refuses new dispatch; this pilot does not rotate or delete records.

## Generic Chat stream negotiation

An explicit JSON body `stream: true` or `false` selects SSE or JSON respectively,
regardless of client `Accept`. If omitted, this ordinary Chat path prefers SSE
unless `Accept` contains `application/json` and not `text/event-stream`. This
matches the literal checks in 9router `39a793a2`; it does not rank MIME quality
values or detect special client tools. The body keeps its original stream field
(or lack of one). The only upstream `Accept` values are `text/event-stream` or
`*/*` (the baseline Fetch default for non-streaming); client headers are not
copied upstream.

If an upstream instead returns valid Chat JSON when neither the stream field nor
an explicit SSE preference was supplied, the pilot preserves that JSON response.
It does not convert JSON to SSE or aggregate SSE into JSON; explicit mismatches
are rejected. This is a local compatibility boundary, not a claim of response
transformation parity with 9router.

## Optional outbound headers for the fixed origin

Set `GO_ROUTER_GENERIC_CHAT_HEADERS_JSON` in the local operator-managed startup
environment, for example `{"X-Tenant":"tenant-a","User-Agent":"my-gateway"}`.
Do not put secret values in command arguments, shell history, or the node
configuration. Header values are not exposed in the model catalog, readiness
check or dispatch journal. Changing the startup snapshot requires a restart.
Unset means no override; an explicit object is required when set, and supplying
it without the generic Chat pilot is an error.

Only configured values go to the fixed HTTPS origin; requests cannot supply
them and they are not sent in proxy CONNECT. In particular auth, Host,
Accept/Accept-Encoding, framing, hop-by-hop, proxy, cookie and forwarding
identity headers cannot be overridden. Invalid or reserved entries refuse
startup rather than being silently dropped. The JSON object is limited to
16 KiB and 16 entries with valid HTTP token names (max 128 bytes), printable
ASCII string values (max 2 KiB), and an 8 KiB total header budget. Outer spaces
are trimmed; an explicit User-Agent cannot be empty. Exact repeated keys are
rejected; with distinct case spellings only the last value is sent, matching
the case-insensitive override in 9router `39a793a2`. The Go pilot does
not support environment interpolation; placeholders starting with `${`
are rejected, never resolved or sent literally.

## Optional strict egress for this generic Chat connection

Leave `GO_ROUTER_GENERIC_CHAT_STRICT_PROXY_URL` **unset** for the original direct
transport. To require a proxy, set it in the operator environment before startup,
for example `GO_ROUTER_GENERIC_CHAT_STRICT_PROXY_URL=https://proxy.example:8443`.
Accepts only an anonymous HTTP or HTTPS CONNECT proxy **origin**, not arbitrary
headers, credentials, path, query, fragment, SOCKS, PAC or relay URLs. HTTPS
also verifies the proxy's certificate; both modes still verify the upstream
TLS certificate and use the same fixed operator-selected Chat endpoint. The
strict mode uses HTTP/1.1 with one tunnel per request, not HTTP/2 multiplexing. Prefer
HTTPS on untrusted networks; an HTTP proxy sees the CONNECT authority.

A present but empty/invalid value refuses startup. An unavailable, rejecting or
untrusted proxy does not retry directly, switch to another proxy, follow a redirect,
or disclose the proxy diagnostic. `HTTP_PROXY`, `HTTPS_PROXY`, and `NO_PROXY`
are not inputs to either mode. Requests cannot select or override this policy.
`/readyz` still means local readiness, not a live proxy/authentication check.

Tunnel setup (including waiting for a connection slot, dialing, CONNECT, and TLS
on both legs) shares a 10-second budget within the existing 30-second Chat request
budget. A client disconnect also aborts an unfinished tunnel without waiting for
a response from the proxy. Up to 16 request tunnels may be dialing or active for
this fixed route. After setup, streaming retains the original Chat request deadline.

This implements the explicit strict-proxy failure boundary in
`codeasier/9router` at `39a793a282629ad8f1bfd6ce703e5dad26be700a`
(`open-sse/utils/proxyFetch.js`), not its pool selection, bypass patterns,
authenticated proxies, environment precedence, provider-specific transports,
or direct fallback modes. No Dashboard or live supplier acceptance is implied.

## Scoped request normalization

For generic OpenAI Chat, exact `developer` roles become `system`, an empty
`tools` array is omitted when `messages` is an array, and `stream_options`
is omitted on the effective non-streaming route. Nonempty tools, multimodal
content and unrelated extension values remain intact. For generic OpenAI-compatible Chat only, supported `json_schema` requests
use the pinned executor’s schema instruction and `json_object` fallback.
Ordinary OpenAI tool-call correlation follows the pinned identifier and
missing-result rules for supported inputs. The router never executes tools or
creates new calls. Unbounded/malformed inputs are outside this slice.

## Offline acceptance

```sh
go build -o /tmp/go-router ./cmd/go-router
python3 scripts/smoke-generic-chat.py /tmp/go-router
```

The test uses a private temporary journal, a local TLS fixture, and dummy credentials. It compares the actual sent body to the journal digest and verifies JSON, SSE, truncation and redirect rejection against a built binary. It does not send traffic to a real supplier.

The standard `go test ./cmd/go-router` suite additionally builds the real binary and uses local HTTP/HTTPS CONNECT and TLS fixtures to check enforced tunneling, closure of a still-pending CONNECT on cancellation, credential separation, rejection without direct fallback, invalid configuration, and untrusted TLS peers.
