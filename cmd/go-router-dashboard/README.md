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
setting them does not apply a configuration. The preflight below derives those values from a validated local artifact so
you do not need to hash it by hand. JSON whitespace and trailing newlines
count. Do not derive the intended digest from the status being checked.

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

## Preview and check one intended artifact

For a local pilot that intends to give both nodes the same configuration,
keep one immutable, credential-free candidate JSON file under operator
control. Plan the distribution **before** moving any files:

```sh
go-router-dashboard preflight -config dashboard-local.json -candidate node.json \
  -node node-a -node node-b
```

This validates the same schema as the gateway and reads the existing private
inventory. It prints one versioned JSON plan with `target.desired_revision`,
`target.desired_digest`, the explicit node IDs and `converged: null`. It does
not resolve bearer environment variables, connect to nodes or write files.
The two `target` values can be put into the selected inventory rows to pin the
same intent in the live Dashboard. Keep addresses and credentials out of the
candidate; only the intended pair, safe node IDs and status are reported.

After the operator separately applies those exact bytes, append `-check`.
With the regular Dashboard bearer environment, the command makes one
authenticated status GET per selected loopback node, in parallel. It does
not compare to existing desired values in the inventory or trust any other
node as the expected source. Each receipt states its observation and comparison
and provides the applied identity when available. Missing digest evidence is
**Unverified**. An unsuccessful status request is **Offline** (not proof the
process stopped). A non-routable node may have an exact match but report
**Not ready**.

Exit codes when running the compiled binary:

- **0**: validated preview, or every requested receipt matched under `-check`.
- **1**: at least one requested receipt did not match (JSON still written), or
  an interruption or output error prevented reporting.
- **2**: invalid command, inventory or candidate (no plan; fixed diagnostics
  on stderr).

Preflight is not a configuration write, publication, ingress gate, or proof of
inference. It does not copy the candidate or approve a future apply. Do not edit
or reserialize that artifact between the preview and any separately authorized
distribution; re-run the check against the original trusted file after applying.
