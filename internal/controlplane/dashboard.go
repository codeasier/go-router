// Package controlplane provides operator views. It never participates in inference.
package controlplane

import (
	"bytes"
	"context"
	"embed"
	"html/template"
	"net/http"
	"regexp"
	"sort"
	"time"
)

// NodeStatus is the documented response projection for GET /admin/v1/status.
// A nil AppliedRevision means the gateway did not report an applied revision.
type NodeStatus struct {
	NodeID          string  `json:"node_id"`
	Ready           bool    `json:"ready"`
	AppliedRevision *string `json:"applied_revision"`
}

// NodeSnapshot identifies a registered node independently of the last response.
// LastSeen is the controller's receipt time, not a remote clock. Offline means
// the latest poll failed; a retained status is then historical, not current.
type NodeSnapshot struct {
	NodeID          string
	DesiredRevision *string
	LastStatus      *NodeStatus
	LastSeen        time.Time
	Offline         bool
}

// SnapshotSource reads an inventory snapshot. Implementations must not expose
// credentials, endpoints, or raw gateway payloads through these fields.
type SnapshotSource interface {
	Snapshots(context.Context) ([]NodeSnapshot, error)
}

//go:embed dashboard.html
var dashboardFiles embed.FS

var fleetPage = template.Must(template.ParseFS(dashboardFiles, "dashboard.html"))

// Keep URLs, arbitrary error strings, and control characters out of the operator
// projection. IDs and revisions are opaque display identifiers, not free text.
var identifier = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,127}$`)

type fleetRow struct {
	Node, Observation, Desired, Applied, Comparison, Seen string
}

// NewDashboard returns a read-only view at /. The caller supplies a trusted
// snapshot source and maximum observation age, and owns listener access control.
// No request sent to this handler polls gateways, writes files, or changes state
// in this package. A collector may independently feed the snapshot source.
func NewDashboard(source SnapshotSource, maxAge time.Duration) http.Handler {
	return dashboard(source, maxAge, time.Now)
}

func dashboard(source SnapshotSource, maxAge time.Duration, clock func() time.Time) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; base-uri 'none'; frame-ancestors 'none'; form-action 'none'")
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if source == nil || maxAge <= 0 {
			http.Error(w, "Fleet snapshot unavailable", http.StatusServiceUnavailable)
			return
		}
		nodes, err := source.Snapshots(r.Context())
		if err != nil {
			http.Error(w, "Fleet snapshot unavailable", http.StatusServiceUnavailable)
			return
		}
		now := clock()
		rows := make([]fleetRow, 0, len(nodes))
		seen := make(map[string]bool, len(nodes))
		for _, node := range nodes {
			if !identifier.MatchString(node.NodeID) || seen[node.NodeID] {
				http.Error(w, "Fleet inventory invalid", http.StatusServiceUnavailable)
				return
			}
			seen[node.NodeID] = true
			rows = append(rows, project(node, now, maxAge))
		}
		sort.Slice(rows, func(i, j int) bool { return rows[i].Node < rows[j].Node })
		var body bytes.Buffer
		if err := fleetPage.ExecuteTemplate(&body, "dashboard.html", rows); err != nil {
			http.Error(w, "Fleet view unavailable", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if r.Method == http.MethodGet {
			_, _ = w.Write(body.Bytes())
		}
	})
}

func revision(value *string) string {
	if value == nil || !identifier.MatchString(*value) {
		return ""
	}
	return *value
}

func project(node NodeSnapshot, now time.Time, maxAge time.Duration) fleetRow {
	row := fleetRow{Node: node.NodeID, Desired: revision(node.DesiredRevision), Comparison: "Unverified"}
	if node.Offline {
		row.Observation = "Offline"
	} else {
		row.Observation = "Not observed"
	}
	status := node.LastStatus
	if status == nil {
		return row
	}
	// Do not attach another instance's status or a future timestamp to this row.
	if node.NodeID != status.NodeID || node.LastSeen.IsZero() || node.LastSeen.After(now) {
		if !node.Offline {
			row.Observation = "Invalid observation"
		}
		return row
	}
	row.Applied = revision(status.AppliedRevision)
	row.Seen = node.LastSeen.UTC().Format(time.RFC3339)
	if node.Offline {
		return row
	}
	if now.Sub(node.LastSeen) > maxAge {
		row.Observation = "Stale"
		return row
	}
	if status.Ready {
		row.Observation = "Ready"
	} else {
		row.Observation = "Not ready"
	}
	if row.Desired != "" && row.Applied != "" {
		if row.Desired == row.Applied {
			row.Comparison = "Matched"
		} else {
			row.Comparison = "Drift"
		}
	}
	return row
}
