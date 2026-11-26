# Local read-only Dashboard pilot

Run this command separately from the gateway processes. This pilot requires the
Dashboard and management listeners to share the same host. It does not poll
remote hosts, relay inference, modify gateway configuration, or expose a public
Dashboard. Local users able to reach its port can read fleet status.

Create a private inventory file (use `umask 077` and `chmod 600` if necessary).
This sample contains environment variable names, not credentials:

```json
{
  "nodes": [
    {
      "node_id": "node-a",
      "admin_addr": "127.0.0.1:20181",
      "bearer_env": "GO_ROUTER_DASHBOARD_KEY_A",
      "desired_revision": null
    },
    {
      "node_id": "node-b",
      "admin_addr": "127.0.0.1:20182",
      "bearer_env": "GO_ROUTER_DASHBOARD_KEY_B",
      "desired_revision": null
    }
  ]
}
```

Use a private directory you control. Provide the two key values from your
trusted local secret environment. Each must match the separately configured
`GO_ROUTER_MANAGEMENT_KEY` for its gateway. Do not put either value in this
file, a shell command or a commit. Gateway `-node-id` and `-admin-addr` must match
the inventory. The optional `desired_revision` and `desired_digest` are per-node targets;
setting them does not apply a configuration. For a verified target, hash the
trusted immutable node configuration file **before** distribution (for example,
`sha256sum node.json`). Set its exact revision and the 64 lowercase hex
characters of that SHA-256 in the inventory. JSON whitespace and trailing
newlines count. Do not copy the digest from the node being checked.

Upgrade the Dashboard binary with its old inventory first, then the nodes,
then add the intended digests to the private inventory and restart the
Dashboard. Missing either side's digest leaves the row **Unverified**. A fresh
complete different pair is **Drift**; a fresh equal pair is **Matched**. This
compares configuration identity, not routability. Offline or stale nodes never
confirm a match. Remove new inventory fields before reverting to an older
Dashboard binary (its inventory parser rejects unknown fields).

From the repository root:

```sh
go run ./cmd/go-router-dashboard -config ./dashboard-local.json -addr 127.0.0.1:20129
```

Open `http://127.0.0.1:20129/` using the exact numeric address; hostnames are
not accepted. The collector polls each node independently every two seconds
after its last check, with a one-second timeout. Refresh the page to read
the latest snapshot. Stopping one node should only mark that row offline;
restarting it should restore its current observation. A gateway with only accepted metadata
can be **Matched** while **Not ready**; without a configuration it has an
unknown applied revision and is **Unverified**. Stopping this Dashboard does not stop
either gateway.
