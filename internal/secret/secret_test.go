package secret

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRedactString(t *testing.T) {
	rd := NewRedactor([]string{"abc"})
	require.Equal(t, "Bearer ***", rd.String("Bearer abc"))
}

func TestRedactBytes(t *testing.T) {
	rd := NewRedactor([]string{"s3cret"})
	require.Equal(t, []byte(`{"t":"***"}`), rd.Bytes([]byte(`{"t":"s3cret"}`)))
}

// Longest-first ordering: redacting "abc" must not leave "def" of "abcdef" exposed.
func TestRedactPrefersLongestSecret(t *testing.T) {
	rd := NewRedactor([]string{"abc", "abcdef"})
	require.Equal(t, "***", rd.String("abcdef"))
}

// A nil or empty Redactor must be safe to call — every consumer holds one
// unconditionally, so there is no "redaction disabled" nil-check at each call site.
func TestRedactorNilAndEmptyAreSafe(t *testing.T) {
	var rd *Redactor
	require.Equal(t, "plain", rd.String("plain"))
	require.Equal(t, "plain", NewRedactor(nil).String("plain"))
}

// Empty strings must never be redacted — replacing "" would corrupt every output.
func TestRedactIgnoresEmptySecret(t *testing.T) {
	require.Equal(t, "hello", NewRedactor([]string{""}).String("hello"))
}
