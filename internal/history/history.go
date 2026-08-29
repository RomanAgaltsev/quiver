// Package history stores an append-only JSONL log of executed requests.
package history

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/RomanAgaltsev/quiver/internal/secret"
)

// Record is one executed request. It carries anough to re-run the request, which
// is what makes `qv history replay` possible.
type Record struct {
	ID       string            `json:"id"`
	Time     time.Time         `json:"time"`
	Name     string            `json:"name"`
	Protocol string            `json:"protocol"`
	Status   int               `json:"status"`
	OK       bool              `json:"ok"`
	Duration string            `json:"duration"`
	Path     string            `json:"path"`           // source request file
	Env      string            `json:"env,omitempty"`  // environment used
	Vars     map[string]string `json:"vars,omitempty"` // --var overrides (redacted)
}

type Store struct {
	path string
	red  *secret.Redactor
}

// Open returns a Store writing to <dir>/history.jsonl, creating dir if needed.
// red may be nil (redact nothing); it is never nil in production.
func Open(dir string, red *secret.Redactor) (*Store, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("history: mkdir: %w", err)
	}
	return &Store{path: filepath.Join(dir, "history.jsonl"), red: red}, nil
}

// NewID returns a time-sortable identifier: a UTC timestamp prefix plus four
// random bytes. The previous revision used six purely random bytes, so history
// had no meaningful order and IDs could not be scanned chronologically.
func NewID() string {
	var b [4]byte
	_, _ = rand.Read(b[:])
	return fmt.Sprintf("%s-%s", time.Now().UTC().Format("20060102T150405.000"), hex.EncodeToString(b[:]))
}

// Append writes one record, redacting any secret that reached the overrides.
// A history file lives next to a git repository; a token written here in the
// clear is the worst possible first bug for a tool selling git-friendliness.
func (s *Store) Append(rec Record) error {
	if len(rec.Vars) > 0 {
		redacted := make(map[string]string, len(rec.Vars))
		for k, v := range rec.Vars {
			redacted[k] = s.red.String(v)
		}
		rec.Vars = redacted
	}

	f, err := os.OpenFile(s.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("history: open: %w", err)
	}
	defer func() { _ = f.Close() }()
	line, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("history: marshal: %w", err)
	}
	if _, err := f.Write(append(s.red.Bytes(line), '\n')); err != nil {
		return fmt.Errorf("history: write: %w", err)
	}
	return nil
}

func (s *Store) List() ([]Record, error) {
	f, err := os.Open(s.path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("history: open: %w", err)
	}
	defer func() { _ = f.Close() }()
	var recs []Record
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var rec Record
		// A single corrupt line (an interrupted write, a hand edit) must not make
		// the entire history unreadable — this file is append-only and long-lived.
		if err := json.Unmarshal(line, &rec); err != nil {
			continue
		}
		recs = append(recs, rec)
	}
	return recs, sc.Err()
}

func (s *Store) Get(id string) (Record, error) {
	recs, err := s.List()
	if err != nil {
		return Record{}, err
	}
	for _, r := range recs {
		if r.ID == id {
			return r, nil
		}
	}
	return Record{}, fmt.Errorf("history: record %q not found", id)
}
