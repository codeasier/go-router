# Offline OpenAI-compatible Chat protocol slice

This adapter is not mounted in the executable. It adds no provider, credential
source, outbound network access, production configuration, retry, or billing.
It covers a synthetic **custom OpenAI-compatible API-key** contract for #6;
VolceAPI and OpenCode Go are not accepted by this fixture.

The intended dispatcher integration is:

1. Select one operator-approved HTTPS base URL and connection outside client
   request handling. The adapter validates the syntax and appends
   `/chat/completions`. For example, `https://host.invalid/api/v1` uses
   `/api/v1/chat/completions`. No URL, credential or header from the client
   selects an endpoint. URL syntax validation is **not** network approval; the
   future dispatcher must still enforce destination rules and DNS/address/TLS
   handling.
2. Prepare bounded JSON with the catalog's upstream model name. All other
   fields, including tools, multimodal inputs and provider extensions, remain
   JSON values. Select one connection-bound Bearer separately and build the
   request. Construction has no external effect.
3. Commit intent using the prepared request's exact body digest. Only after
   success may the dispatcher perform one transport call, with a bounded
   deadline, without redirects or replay. The adapter owns no transport and
   returns no retry authorization. Never log the generated HTTP request or
   body.
4. Decode the response, which always closes the response body. Only complete
   non-stream JSON or delimited SSE data frames reach the callback. A complete
   SSE sequence requires a choice followed by `[DONE]`. Non-success HTTP,
   in-stream errors, malformed/oversized frames and incomplete streams never
   establish successful completion. HTTP status is only protocol evidence;
   nothing here proves whether an upstream executed or billed the request.
   Provider error bodies, redirect targets and downstream callback errors
   are not relayed or included in diagnostics. Missing, ambiguous or malformed
   usage remains unknown. SSE only confirms usage in a separate final
   usage frame.

Local fixture checks: `go test -race ./internal/openaichat ./internal/syntheticchat`.
The test-only dispatcher runs against a TLS server on loopback. It checks
the exact auth/path and verifies the journal receipt and wire digest when
the server receives the request. A redirect receives no second request and
a failed journal yields no additional request. The two independent wire
fixtures cover JSON and SSE, including tool calls and usage. These tests
never contact a provider and do not establish live provider parity.
