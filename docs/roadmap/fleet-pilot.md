# Two-instance operations pilot

Status: **planned**. This is the first fleet milestone, not production release approval.

## Order

1. **With M1:** define stable instance ID, capabilities, liveness/readiness, desired/applied config revision, and usage and quota event identities. Run one synthetic end-to-end chat path with a durable dispatch intent and cancellation tests before broad provider expansion.
2. **Minimal control plane + Dashboard:** register two test nodes, preview a model add/remove, apply a versioned revision, inspect partial apply and retry safely. Keep authentication, audit and apply confirmation separate from inference.
3. **Operating validation:** reconcile route eligibility and weights via a test ingress, verify drain and rollback, and compare node-level and fleet-level usage and quota. Downstream provider credentials stay out of the Dashboard. The first pilot uses synthetic or explicitly authorized test data only.

The Dashboard and controller run separately from the gateway binary, but all three are delivered from this repository. The traffic ingress stays in the forwarding path; the controller changes policy, never handles the live stream. This milestone does not depend on browser/web-cookie, media, every provider, or advanced UI.

## Local configuration readback

This development step tests revision readback on one host. It does not
publish configuration from the Dashboard, change existing routes, or connect
to any model provider. The node reports the active local configuration
revision in its authenticated `/admin/v1/status`; in the default metadata-only mode it
still reports `ready: false`, and `/v1/models` remains empty. A node started
without a configuration reports both `applied_revision: null` and
`applied_digest: null`. A configured node reports the revision and lowercase
SHA-256 of the exact accepted file bytes from one snapshot. The digest is
withheld if it would echo the management credential.

Pass `-config /path/to/node.json` to opt in. For example:

```json
{"schema_version":1,"revision":"pilot-001","providers":[{"id":"synthetic","transport":"chat","models":[{"id":"demo"}]}]}
```

The file is local metadata (JSON, up to 1 MiB), not the full runtime
configuration. It must not contain credentials or upstream URLs. Keep it
in a directory controlled by the operator. Start with a valid file. To change it, write a complete replacement alongside the old file,
rename it atomically over the configured path, then send `SIGHUP` to that
node on Unix. Wait for readback before sending another signal; pending
reload signals can coalesce. Use a new revision for each content change,
including a rollback to an older payload. Re-reading identical bytes for the active revision is a no-op.
The process rejects reused labels and limits history to 1,024 accepted
revisions; history does not survive restart. An invalid update keeps the
active revision. Check the authenticated status or the local Dashboard
afterward. The local Dashboard compares each node's fresh revision **and**
digest against its own inventory target, not another node or the label alone.
Only a complete matching pair shows **Matched**; a complete but different pair
shows **Drift**. Legacy nodes or targets without a digest show **Unverified**.
This is evidence of local acceptance, not remote publication, ingress policy
or inference readiness.

## Acceptance

- Add one model in the Dashboard once. Both eligible nodes confirm applying the same revision and serve the intended model. If one node rejects the change, show intended versus applied revision, error and stale telemetry status. Exclude incompatible nodes from new traffic for the affected route without unnecessarily removing healthy unrelated routes.
- Drain one node: no new model requests go to it, and already admitted streams may complete within a bounded drain. An uncertain upstream execution must not be silently replayed elsewhere.
- Deduplicate retried reporting by stable event ID; verify node and fleet totals. For an upstream account shared by two instances, show one quota observation per account/metric/window, with freshness; independent accounts remain distinguishable. Unknown is not zero.
- Roll back to a previously verified payload using a new revision and verify per-node acknowledgment and ingress reconciliation. No split revision should be presented as a successful apply.
- Stop the Dashboard/controller and verify established data-plane routes keep serving the last valid local configuration. Recover management independently and reconcile drift. Test auth boundaries and diagnostic redaction on both sides.

Only after isolated proof should a separately authorized trial use existing live infrastructure. Production cutover, release and rollback rehearsal are distinct approvals.
