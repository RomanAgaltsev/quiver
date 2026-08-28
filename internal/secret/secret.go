// Package secret replaces secret values with a placeholder in anything that is
// rendered to a terminal or persisted to disk.
package secret

import (
	"bytes"
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
	kept := make([]string, 0, len(secrets))
	for _, s := range secrets {
		if s != "" {
			kept = append(kept, s)
		}
	}
	// Longest first, so a short secret that is a substring of a longer one cannot
	// partially redact it and leave the tail exposed.
	sort.Slice(kept, func(i, j int) bool {
		return len(kept[i]) > len(kept[j])
	})
	return &Redactor{secrets: kept}
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
