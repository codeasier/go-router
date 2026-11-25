package controlplane

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	statusPath    = "/admin/v1/status"
	maxPilotNodes = 16
	statusLimit   = 4 << 10
)

var (
	pilotNodeID  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
	pilotBearer  = regexp.MustCompile(`^[A-Za-z0-9._~+/-]+=*$`)
	errBadStatus = errors.New("invalid node status")
)

// CollectorNode is trusted, private local configuration. It must not be
// serialized into page data or diagnostic logs.
type CollectorNode struct {
	NodeID          string
	AdminAddr       string // Numeric loopback host:port, not a URL.
	BearerKey       string
	DesiredRevision *string
}

type registeredNode struct {
	endpoint, bearer string
}

// Collector independently polls registered local nodes and serves in-memory
// snapshots. It does not route inference, discover endpoints, or apply config.
type Collector struct {
	client   *http.Client
	interval time.Duration
	nodes    []registeredNode
	bearers  map[string]struct{}
	running  atomic.Bool

	mu        sync.RWMutex
	snapshots []NodeSnapshot
}

// NewCollector creates a loopback-only pilot collector without starting polls.
// Credentials are used only for GET requests to the configured local endpoint.
// To poll nodes on other hosts a separately designed authenticated transport is
// required; plain HTTP must not be enabled by loosening the address check.
func NewCollector(nodes []CollectorNode, interval, timeout time.Duration) (*Collector, error) {
	if len(nodes) == 0 || len(nodes) > maxPilotNodes || interval <= 0 || timeout <= 0 {
		return nil, errors.New("invalid pilot inventory size, poll interval, or timeout")
	}
	transport := &http.Transport{
		DialContext:            (&net.Dialer{Timeout: timeout}).DialContext,
		DisableCompression:     true,
		IdleConnTimeout:        30 * time.Second,
		MaxConnsPerHost:        1,
		MaxIdleConns:           maxPilotNodes,
		MaxIdleConnsPerHost:    1,
		MaxResponseHeaderBytes: 16 << 10,
	}
	c := &Collector{
		client: &http.Client{
			Transport: transport,
			Timeout:   timeout,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		interval:  interval,
		nodes:     make([]registeredNode, 0, len(nodes)),
		bearers:   make(map[string]struct{}, len(nodes)),
		snapshots: make([]NodeSnapshot, 0, len(nodes)),
	}
	for _, n := range nodes {
		if len(n.BearerKey) < 32 || len(n.BearerKey) > 256 || !pilotBearer.MatchString(n.BearerKey) {
			return nil, errors.New("invalid pilot management bearer")
		}
		c.bearers[n.BearerKey] = struct{}{}
	}
	addrs, ids := make(map[netip.AddrPort]bool, len(nodes)), make(map[string]bool, len(nodes))
	for _, n := range nodes {
		if !pilotNodeID.MatchString(n.NodeID) || ids[n.NodeID] || c.containsManagementKey(n.NodeID) {
			return nil, errors.New("invalid or duplicate pilot node ID")
		}
		ids[n.NodeID] = true
		addr, err := netip.ParseAddrPort(n.AdminAddr)
		if err != nil || addr.Port() == 0 || addr.Addr().Zone() != "" || !addr.Addr().IsLoopback() {
			return nil, errors.New("pilot management address must be a numeric loopback address with a nonzero port")
		}
		addr = netip.AddrPortFrom(addr.Addr().Unmap(), addr.Port())
		if addrs[addr] {
			return nil, errors.New("duplicate pilot management address")
		}
		addrs[addr] = true
		if n.DesiredRevision != nil && !c.safeRevision(n.DesiredRevision) {
			return nil, errors.New("invalid pilot desired revision")
		}
		endpoint := url.URL{Scheme: "http", Host: addr.String(), Path: statusPath}
		c.nodes = append(c.nodes, registeredNode{endpoint: endpoint.String(), bearer: n.BearerKey})
		c.snapshots = append(c.snapshots, NodeSnapshot{
			NodeID: n.NodeID, DesiredRevision: copyRevision(n.DesiredRevision),
		})
	}
	return c, nil
}

// Run polls immediately and then after each node's independent interval, until
// ctx is canceled. It waits for all pollers to exit and closes idle connections.
// A single Collector may have only one active Run. Cancellation does not mark
// registered nodes offline: no check completed at that point.
func (c *Collector) Run(ctx context.Context) error {
	if !c.running.CompareAndSwap(false, true) {
		return errors.New("pilot collector is already running")
	}
	defer c.running.Store(false)
	defer c.client.CloseIdleConnections()
	var wg sync.WaitGroup
	for i, node := range c.nodes {
		wg.Go(func() { c.runNode(ctx, i, node) })
	}
	wg.Wait()
	return ctx.Err()
}

func (c *Collector) runNode(ctx context.Context, index int, node registeredNode) {
	for ctx.Err() == nil {
		status, err := c.poll(ctx, node, c.snapshots[index].NodeID)
		if ctx.Err() != nil {
			return
		}
		now := time.Now()
		c.mu.Lock()
		if ctx.Err() == nil {
			row := &c.snapshots[index]
			row.Offline = err != nil
			if err == nil {
				row.LastSeen = now
				row.LastStatus = status
			}
		}
		c.mu.Unlock()
		timer := time.NewTimer(c.interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

func (c *Collector) poll(ctx context.Context, node registeredNode, wantID string) (*NodeStatus, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, node.endpoint, nil)
	if err != nil {
		return nil, errBadStatus
	}
	req.Header.Set("Authorization", "Bearer "+node.bearer)
	req.Header.Set("Accept", "application/json")
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, errBadStatus
	}
	defer resp.Body.Close()
	mediaType, _, e := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if resp.StatusCode != http.StatusOK || e != nil || mediaType != "application/json" {
		return nil, errBadStatus
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, statusLimit+1))
	if err != nil || len(body) > statusLimit {
		return nil, errBadStatus
	}
	fields, err := statusFields(body)
	if err != nil {
		return nil, errBadStatus
	}
	var id string
	var ready *bool
	var rev *string
	if json.Unmarshal(fields["node_id"], &id) != nil || id != wantID ||
		json.Unmarshal(fields["ready"], &ready) != nil || ready == nil ||
		json.Unmarshal(fields["applied_revision"], &rev) != nil || !c.safeRevision(rev) {
		return nil, errBadStatus
	}
	return &NodeStatus{NodeID: id, Ready: *ready, AppliedRevision: rev}, nil
}

func statusFields(body []byte) (map[string]json.RawMessage, error) {
	d := json.NewDecoder(bytes.NewReader(body))
	if token, err := d.Token(); err != nil || token != json.Delim('{') {
		return nil, errBadStatus
	}
	fields := make(map[string]json.RawMessage)
	for d.More() {
		k, err := d.Token()
		key, isString := k.(string)
		if err != nil || !isString || fields[key] != nil {
			return nil, errBadStatus
		}
		var raw json.RawMessage
		if d.Decode(&raw) != nil {
			return nil, errBadStatus
		}
		fields[key] = raw
	}
	if token, err := d.Token(); err != nil || token != json.Delim('}') {
		return nil, errBadStatus
	}
	if _, err := d.Token(); err != io.EOF {
		return nil, errBadStatus
	}
	return fields, nil
}

func (c *Collector) safeRevision(r *string) bool {
	if r == nil {
		return true
	}
	return identifier.MatchString(*r) && !c.containsManagementKey(*r)
}

// Only call this on bounded display identifiers. Reject an echoed credential
// even when wrapped in an otherwise valid ID or revision.
func (c *Collector) containsManagementKey(value string) bool {
	for key := range c.bearers {
		if strings.Contains(value, key) {
			return true
		}
	}
	return false
}

func copyRevision(r *string) *string {
	if r == nil {
		return nil
	}
	s := *r
	return &s
}

// Snapshots implements SnapshotSource. Returned values can be read or modified
// without mutating the collector or retaining pointers to its stored status.
func (c *Collector) Snapshots(ctx context.Context) ([]NodeSnapshot, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	rows := make([]NodeSnapshot, len(c.snapshots))
	for i, row := range c.snapshots {
		rows[i] = row
		rows[i].DesiredRevision = copyRevision(row.DesiredRevision)
		if row.LastStatus != nil {
			status := *row.LastStatus
			status.AppliedRevision = copyRevision(status.AppliedRevision)
			rows[i].LastStatus = &status
		}
	}
	return rows, nil
}
