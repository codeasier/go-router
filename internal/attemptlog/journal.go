// Package attemptlog records dispatch intent before an inference transport is
// called. It is a bounded, single-node M1 primitive, not a retry queue. A
// recovered intent is ambiguous: it proves neither transmission nor execution.
// There is deliberately no automatic replay, pruning, rotation or settlement.
package attemptlog

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"sync"
	"time"
)

const (
	filename = "dispatch-intents.v1"
	header   = "go-router/dispatch-intents/v1\n"
	maxLine  = 4096
	maxBytes = 32 << 20 // Refuse new dispatch, never evict old receipts.
)

var (
	ErrUnavailable = errors.New("dispatch intent journal unavailable")
	ErrInvalid     = errors.New("invalid dispatch intent metadata")
	ErrDuplicate   = errors.New("dispatch attempt already recorded")
	ErrCorrupt     = errors.New("invalid dispatch intent journal; refusing to recover")
	ErrFull        = errors.New("dispatch intent journal capacity reached")
)

// Intent accepts only locally assigned IDs and registered catalog references.
// Never pass credentials, prompts, URLs, headers, or client-supplied free text
// as identifiers. BodyDigest is the SHA-256 digest, not the request body.
type Intent struct {
	RequestID    string            `json:"request_id"`
	AttemptID    string            `json:"attempt_id"`
	Provider     string            `json:"provider"`
	Model        string            `json:"model"`
	ConnectionID string            `json:"connection_id"`
	Operation    string            `json:"operation"`
	BodyDigest   [sha256.Size]byte `json:"body_digest"`
}

// Record has no execution result. Until settlement exists, the only safe
// interpretation of a recorded attempt without other evidence is UNKNOWN.
type Record struct {
	Intent    Intent `json:"intent"`
	UnixMilli int64  `json:"unix_milli"`
}

type syncWriter interface {
	io.Writer
	Sync() error
}

// Journal must not be copied after opening. A mutex serializes callers and a
// nonblocking advisory lock excludes other cooperating owners of the same file.
type Journal struct {
	mu     sync.Mutex
	file   *os.File
	writer syncWriter
	seen   map[string]Record
	size   int64
	failed bool
}

// Create exclusively initializes the fixed journal filename in an existing
// owner-only directory. Ancestors and directory entries must remain under
// trusted operator control. Never call Create as a fallback for failed Open.
// Requires a local filesystem honoring advisory locks and file/directory sync.
func Create(dir string) (*Journal, error) { return open(dir, true) }

// Open recovers an existing journal, never creates or repairs one. Missing,
// corrupt, truncated and unrecognized records are not treated as empty. Like
// any standalone append log it cannot detect external rollback to a previous
// complete copy; backups and deliberate recovery require operator coordination.
func Open(dir string) (*Journal, error) { return open(dir, false) }

func open(dir string, create bool) (*Journal, error) {
	d, f, err := openOwnedFile(dir, create)
	if err != nil {
		return nil, err
	}
	defer d.Close()
	ok := false
	defer func() {
		if !ok {
			_ = f.Close()
		}
	}()
	j := &Journal{file: f, writer: f, seen: make(map[string]Record)}
	if create {
		if !writeSync(f, []byte(header)) {
			return nil, ErrUnavailable
		}
		j.size = int64(len(header))
	} else {
		if err := j.recover(); err != nil {
			return nil, err
		}
		// A previous process may have died after writing but before syncing.
		if f.Sync() != nil {
			return nil, ErrUnavailable
		}
	}
	// Also sync the directory on reopen after an interrupted initialization.
	if d.Sync() != nil {
		return nil, ErrUnavailable
	}
	ok = true
	return j, nil
}

func validID(s string) bool {
	if len(s) == 0 || len(s) > 128 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if ('a' <= c && c <= 'z') || ('A' <= c && c <= 'Z') || ('0' <= c && c <= '9') {
			continue
		}
		if i > 0 && (c == '-' || c == '_' || c == '.' || c == '/' || c == ':') {
			continue
		}
		return false
	}
	return !strings.Contains(s, "://")
}

func (in Intent) valid() bool {
	return validID(in.RequestID) && validID(in.AttemptID) && validID(in.Provider) && validID(in.Model) && validID(in.ConnectionID) && validID(in.Operation)
}

func lineFor(r Record) []byte {
	// This concrete struct has no unsupported JSON values.
	data, _ := json.Marshal(r)
	h := sha256.Sum256(data)
	line := make([]byte, hex.EncodedLen(len(h)), len(data)+66)
	hex.Encode(line, h[:])
	line = append(line, '\t')
	line = append(line, data...)
	return append(line, '\n')
}

func (j *Journal) recover() error {
	st, err := j.file.Stat()
	if err != nil {
		return ErrUnavailable
	}
	if st.Size() > maxBytes {
		return ErrFull
	}
	br := bufio.NewReaderSize(j.file, maxLine)
	b, err := br.ReadSlice('\n')
	if err != nil || string(b) != header {
		return ErrCorrupt
	}
	j.size = int64(len(header))
	for {
		line, err := br.ReadSlice('\n')
		if err == io.EOF && len(line) == 0 {
			break
		}
		if err != nil || len(line) < 66 || line[64] != '\t' {
			return ErrCorrupt
		}
		var r Record
		if json.Unmarshal(line[65:len(line)-1], &r) != nil || !r.Intent.valid() || r.UnixMilli <= 0 {
			return ErrCorrupt
		}
		// Canonical representation also rejects missing/unknown/duplicate JSON
		// fields and arrays coerced by encoding/json; digest checks torn records.
		if !bytes.Equal(lineFor(r), line) {
			return ErrCorrupt
		}
		if _, found := j.seen[r.Intent.AttemptID]; found {
			return ErrCorrupt
		}
		j.seen[r.Intent.AttemptID] = r
		j.size += int64(len(line))
	}
	return nil
}

func writeSync(w syncWriter, line []byte) bool {
	n, err := w.Write(line)
	return err == nil && n == len(line) && w.Sync() == nil
}

// RecordIntent is the dispatch barrier. Only a nil return permits the caller
// to proceed to transport once. After any ambiguous write or sync the handle
// fails closed; opening the file again recovers records without resending.
func (j *Journal) RecordIntent(in Intent) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.file == nil || j.failed {
		return ErrUnavailable
	}
	if !in.valid() {
		return ErrInvalid
	}
	if _, found := j.seen[in.AttemptID]; found {
		return ErrDuplicate
	}
	r := Record{Intent: in, UnixMilli: time.Now().UnixMilli()}
	if r.UnixMilli <= 0 {
		return ErrUnavailable
	}
	line := lineFor(r)
	if j.size+int64(len(line)) > maxBytes {
		return ErrFull
	}
	if !writeSync(j.writer, line) {
		j.failed = true
		return ErrUnavailable
	}
	j.seen[in.AttemptID] = r
	j.size += int64(len(line))
	return nil
}

// Lookup returns stored metadata only. A miss is not authorization to resend.
func (j *Journal) Lookup(id string) (Record, bool, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.file == nil || j.failed {
		return Record{}, false, ErrUnavailable
	}
	r, ok := j.seen[id]
	return r, ok, nil
}

func (j *Journal) Close() error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.file == nil {
		return nil
	}
	err := j.file.Close()
	j.file = nil
	if err != nil {
		return ErrUnavailable
	}
	return nil
}
