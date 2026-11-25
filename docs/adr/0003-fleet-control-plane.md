# ADR 0003: lightweight gateway nodes and unified fleet operations

- Status: Accepted for product scope and repository ownership
- Date: 2026-11-25

## Decision

Host the gateway, fleet controller and unified Dashboard in the go-router repository. A gateway node is a lightweight proxy, not a management server for the whole fleet. The controller and Dashboard deploy separately. The fleet Dashboard is a first-class product surface for two-instance operations, not an optional late UI. This changes the Dashboard timing in [ADR 0002](0002-v1-scope.md), not its provider boundaries.

- The controller is the source of desired fleet configuration and displays the per-node applied revision; nodes validate before activation and continue with their last valid configuration when disconnected.
- Configuration, local telemetry, and ingress control are separate interfaces. The controller manages desired weights and drain through an ingress adapter, not by proxying client inference itself. Instance balancing and upstream-account fallback are independent decisions.
- Use stable nonsecret identities for nodes and quota accounts. Distinguish usage events, provider-reported remaining quota and policy budgets. Do not sum duplicate observations of one shared account or imply a strict distributed budget from sampled data.
- Keep node credentials local to the authorized execution path and distribute references rather than plaintext in browser responses or audit records. Authenticate operator access separately from inference keys.
- Existing monitoring and 9router operations are migration references, not runtime dependencies. Keep production deployment state and real fixtures out of this public repository.

## Consequences

The early acceptance milestone is an end-to-end operating scenario with two nodes. It does not require Phase 2 providers or advanced UI. A failed or disconnected node stays eligible only for routes it can safely serve under the policy; do not redirect an in-progress inference as a silent retry. Frontend technology, protocol transport, and ingress integration are separate implementation decisions.

See the [two-instance pilot](../roadmap/fleet-pilot.md) for acceptance and rollout boundaries.
