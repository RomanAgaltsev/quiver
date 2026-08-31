// Package secret replaces secret values with a placeholder in anything that is
// rendered to a terminal or persisted to disk.
package secret

import (
	"bytes"
	"slices"
	"sort"
	"strings"
)

// Placeholder is what a redacted secret is replaced with.
const Placeholder = "***"

// Redactor replaces known secret values with Placeholder. The zero value and a
// nil pointer are both usable and redact nothing, so callers never need a nil check.
type Redactor struct {
	secrets []string
}

// NewRedactor returns a Redactor for the given concrete secret values, typically
// env.Resolved.Secrets. Empty values are dropped: replacing "" would corrupt every
// string it touched.
func NewRedactor(secrets []string) *Redactor {
	rd := &Redactor{}
	rd.Add(secrets...)
	return rd
}

// Add registers further secret values. Secrets are not all known up front: a
// {{env:NAME}} written inside a request file is discovered during resolution,
// long after render and history were handed their redactor, and a resolved but
// unredacted token is strictly worse than an unresolved one.
//
// The CLI is single-threaded, so this needs no locking; a concurrent consumer
// would have to add its secrets before starting work.
func (rd *Redactor) Add(secrets ...string) {
	if rd == nil { // a nil Redactor redacts nothing; adding to it is a no-op
		return
	}
	for _, s := range secrets {
		if s == "" { // replacing "" would corrupt every string it touched
			continue
		}
		if slices.Contains(rd.secrets, s) {
			continue
		}
		rd.secrets = append(rd.secrets, s)
	}
	// Longest first, so a short secret that is a substring of a longer one cannot
	// partially redact it and leave the tail exposed.
	sort.SliceStable(rd.secrets, func(i, j int) bool {
		return len(rd.secrets[i]) > len(rd.secrets[j])
	})
}

// String returns s with every known secret replaced.
func (rd *Redactor) String(s string) string {
	if rd == nil || len(rd.secrets) == 0 {
		return s
	}
	for _, sec := range rd.secrets {
		s = strings.ReplaceAll(s, sec, Placeholder)
	}
	return s
}

// Bytes returns b with every known secret replaced. The input is never mutated.
func (rd *Redactor) Bytes(b []byte) []byte {
	if rd == nil || len(rd.secrets) == 0 || len(b) == 0 {
		return b
	}
	out := b
	for _, sec := range rd.secrets {
		out = bytes.ReplaceAll(out, []byte(sec), []byte(Placeholder))
	}
	return out
}
