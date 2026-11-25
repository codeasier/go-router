# Two-instance operations pilot

Status: **planned**. This is the first fleet milestone, not production release approval.

## Order

1. **With M1:** define stable instance ID, capabilities, liveness/readiness, desired/applied config revision, and usage and quota event identities. Run one synthetic end-to-end chat path with a durable dispatch intent and cancellation tests before broad provider expansion.
2. **Minimal control plane + Dashboard:** register two test nodes, preview a model add/remove, apply a versioned revision, inspect partial apply and retry safely. Keep authentication, audit and apply confirmation separate from inference.
3. **Operating validation:** reconcile route eligibility and weights via a test ingress, verify drain and rollback, and compare node-level and fleet-level usage and quota. Downstream provider credentials stay out of the Dashboard. The first pilot uses synthetic or explicitly authorized test data only.

The Dashboard and controller run separately from the gateway binary, but all three are delivered from this repository. The traffic ingress stays in the forwarding path; the controller changes policy, never handles the live stream. This milestone does not depend on browser/web-cookie, media, every provider, or advanced UI.

## Acceptance

- Add one model in the Dashboard once. Both eligible nodes confirm applying the same revision and serve the intended model. If one node rejects the change, show intended versus applied revision, error and stale telemetry status. Exclude incompatible nodes from new traffic for the affected route without unnecessarily removing healthy unrelated routes.
- Drain one node: no new model requests go to it, and already admitted streams may complete within a bounded drain. An uncertain upstream execution must not be silently replayed elsewhere.
- Deduplicate retried reporting by stable event ID; verify node and fleet totals. For an upstream account shared by two instances, show one quota observation per account/metric/window, with freshness; independent accounts remain distinguishable. Unknown is not zero.
- Roll back to a previously verified payload using a new revision and verify per-node acknowledgment and ingress reconciliation. No split revision should be presented as a successful apply.
- Stop the Dashboard/controller and verify established data-plane routes keep serving the last valid local configuration. Recover management independently and reconcile drift. Test auth boundaries and diagnostic redaction on both sides.

Only after isolated proof should a separately authorized trial use existing live infrastructure. Production cutover, release and rollback rehearsal are distinct approvals.
