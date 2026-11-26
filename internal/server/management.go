package server

import (
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"net/http"
	"net/netip"
	"regexp"
	"strings"
	"time"
)

var (
	nodeIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
	bearerPattern = regexp.MustCompile(`^[A-Za-z0-9._~+/-]+=*$`)
)

// dataPlaneReady describes inference readiness, not process or management liveness.
// Accepted metadata alone does not make the inference path routable.
func dataPlaneReady() bool { return false }

func handleReadyz(w http.ResponseWriter, _ *http.Request) {
	ready := dataPlaneReady()
	status := http.StatusOK
	if !ready {
		status = http.StatusServiceUnavailable
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, status, map[string]bool{"ready": ready})
}

type nodeStatus struct {
	NodeID          string  `json:"node_id"`
	Ready           bool    `json:"ready"`
	AppliedRevision *string `json:"applied_revision"`
}

// NewManagement builds an optional read-only HTTP listener, independent of the
// inference listener. Only a literal loopback address is accepted: this stage
// does not implement secure cross-host transport.
func NewManagement(addr, nodeID, key string) (*Server, error) {
	return NewManagementWithRevision(addr, nodeID, key, nil)
}

// NewManagementWithRevision uses a concurrency-safe source for the current
// accepted local metadata revision. An empty revision is reported as null.
func NewManagementWithRevision(addr, nodeID, key string, appliedRevision func() string) (*Server, error) {
	if appliedRevision == nil {
		return NewManagementWithStatus(addr, nodeID, key, nil)
	}
	return NewManagementWithStatus(addr, nodeID, key, func() (string, bool) {
		return appliedRevision(), false
	})
}

// NewManagementWithStatus reads revision and readiness from one immutable
// generation, so an update cannot mix a previous revision with new routes.
func NewManagementWithStatus(addr, nodeID, key string, current func() (string, bool)) (*Server, error) {
	ap, err := netip.ParseAddrPort(addr)
	if err != nil || !ap.Addr().IsLoopback() || ap.Addr().Zone() != "" || ap.Port() == 0 {
		return nil, errors.New("management address must be a numeric loopback address with a nonzero port")
	}
	if !nodeIDPattern.MatchString(nodeID) {
		return nil, errors.New("node ID must be 1-64 ASCII letters, digits, dots, underscores or hyphens, starting with a letter or digit")
	}
	if len(key) < 32 || len(key) > 256 || !bearerPattern.MatchString(key) {
		return nil, errors.New("management key must be a 32-256 character bearer token")
	}

	// Do not retain the configured bearer in the handler or include it in errors.
	want := sha256.Sum256([]byte(key))
	mux := http.NewServeMux()
	mux.HandleFunc("/admin/v1/status", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if !hasBearer(r, want) {
			w.Header().Set("WWW-Authenticate", "Bearer")
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		status := nodeStatus{NodeID: nodeID, Ready: dataPlaneReady()}
		if current != nil {
			revision, ready := current()
			status.Ready = ready
			if revision != "" {
				status.AppliedRevision = &revision
			}
		}
		writeJSON(w, http.StatusOK, status)
	})
	return &Server{httpServer: &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		WriteTimeout:      5 * time.Second,
		IdleTimeout:       30 * time.Second,
		MaxHeaderBytes:    16 << 10,
	}}, nil
}

func hasBearer(r *http.Request, want [sha256.Size]byte) bool {
	values := r.Header.Values("Authorization")
	if len(values) != 1 {
		return false
	}
	scheme, token, ok := strings.Cut(values[0], " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") || len(token) > 256 {
		return false
	}
	got := sha256.Sum256([]byte(token))
	return subtle.ConstantTimeCompare(got[:], want[:]) == 1
}
