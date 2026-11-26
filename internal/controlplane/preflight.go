package controlplane

import (
	"context"
	"errors"
	"sort"
	"sync"
	"time"
)

// ConfigIdentity describes the revision and exact-byte SHA-256 of one trusted,
// validated local configuration artifact. It contains no artifact bytes.
type ConfigIdentity struct {
	Revision string `json:"desired_revision"`
	Digest   string `json:"desired_digest"`
}

// ConfigReceipt describes one read-only check for a selected registered node.
type ConfigReceipt struct {
	NodeID          string  `json:"node_id"`
	Observation     string  `json:"observation"`
	Comparison      string  `json:"comparison"`
	AppliedRevision *string `json:"applied_revision"`
	AppliedDigest   *string `json:"applied_digest"`
}

// ConfigPreflight binds a single artifact identity to an explicit set of
// registered nodes. A nil Converged means no checks were requested.
type ConfigPreflight struct {
	SchemaVersion int             `json:"schema_version"`
	Target        ConfigIdentity  `json:"target"`
	Nodes         []ConfigReceipt `json:"nodes"`
	Converged     *bool           `json:"converged"`
}

// PreviewConfiguration validates registration metadata and shows the intended
// target identity. It never resolves credentials or contacts any node.
func PreviewConfiguration(ctx context.Context, nodes []CollectorNode, target ConfigIdentity, ids []string) (*ConfigPreflight, error) {
	noKey := func(string) bool { return false }
	if _, err := validateInventory(nodes, noKey); err != nil {
		return nil, err
	}
	known := make(map[string]int, len(nodes))
	for i, n := range nodes {
		known[n.NodeID] = i
	}
	return preflight(ctx, target, known, ids, noKey, nil)
}

// CheckConfiguration validates the complete target set before requesting one
// authenticated status GET per selected node, in parallel. It does not change
// the original inventory or the collector's cached observations or listen.
func (c *Collector) CheckConfiguration(ctx context.Context, target ConfigIdentity, ids []string) (*ConfigPreflight, error) {
	known := make(map[string]int, len(c.nodes))
	for i := range c.nodes {
		known[c.snapshots[i].NodeID] = i // NodeID is immutable, even while Run is active.
	}
	observe := func(ctx context.Context, i int, id string) (*NodeStatus, error) {
		return c.poll(ctx, c.nodes[i], id)
	}
	return preflight(ctx, target, known, ids, c.containsManagementKey, observe)
}

func preflight(ctx context.Context, target ConfigIdentity, known map[string]int, ids []string, containsKey func(string) bool, observe func(context.Context, int, string) (*NodeStatus, error)) (*ConfigPreflight, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !identifier.MatchString(target.Revision) || containsKey(target.Revision) ||
		!validDigest(&target.Digest) || containsKey(target.Digest) {
		return nil, errors.New("invalid preflight configuration identity")
	}
	if len(ids) == 0 || len(ids) > len(known) {
		return nil, errors.New("preflight needs an explicit set of registered nodes")
	}
	order := append([]string(nil), ids...)
	sort.Strings(order)
	for i, id := range order {
		if _, ok := known[id]; !ok || (i > 0 && order[i-1] == id) {
			return nil, errors.New("preflight contains an unknown or duplicate node")
		}
	}
	report := &ConfigPreflight{
		SchemaVersion: 1,
		Target:        target,
		Nodes:         make([]ConfigReceipt, len(order)),
	}
	if observe == nil {
		for i, id := range order {
			report.Nodes[i] = ConfigReceipt{NodeID: id, Observation: "Not checked", Comparison: "Unverified"}
		}
		return report, nil
	}
	// After the single bounded GET, reuse Dashboard comparison semantics. No
	// historical observation can stand in for a failed request in this report.
	var wg sync.WaitGroup
	for i, id := range order {
		wg.Go(func() {
			status, err := observe(ctx, known[id], id)
			now := time.Now()
			row := project(NodeSnapshot{
				NodeID: id, DesiredRevision: &target.Revision, DesiredDigest: &target.Digest,
				LastStatus: status, LastSeen: now, Offline: err != nil,
			}, now, time.Second)
			receipt := ConfigReceipt{NodeID: id, Observation: row.Observation, Comparison: row.Comparison}
			if status != nil {
				receipt.AppliedRevision = status.AppliedRevision
				receipt.AppliedDigest = status.AppliedDigest
			}
			report.Nodes[i] = receipt
		})
	}
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	converged := true
	for _, receipt := range report.Nodes {
		converged = converged && receipt.Comparison == "Matched"
	}
	report.Converged = &converged
	return report, nil
}
