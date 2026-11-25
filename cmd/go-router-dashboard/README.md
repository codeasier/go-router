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
the inventory. An optional desired revision is a display/comparison value;
setting it does not apply a revision to a gateway.

From the repository root:

```sh
go run ./cmd/go-router-dashboard -config ./dashboard-local.json -addr 127.0.0.1:20129
```

Open `http://127.0.0.1:20129/` using the exact numeric address; hostnames are
not accepted. The collector polls each node independently every two seconds
after its last check, with a one-second timeout. Refresh the page to read
the latest snapshot. Stopping one node should only mark that row offline;
restarting it should restore its current observation. A gateway without a
routable configuration should show **Not ready** and an unknown applied
revision, not claim availability. Stopping this Dashboard does not stop
either gateway.
