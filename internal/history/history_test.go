package history

import (
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/RomanAgaltsev/quiver/internal/secret"
)

func TestAppendListGet(t *testing.T) {
	st, err := Open(t.TempDir(), nil)
	require.NoError(t, err)
	rec := Record{
		ID: "abc", Time: time.Now(), Name: "list", Protocol: "http",
		Status: 200, OK: true, Duration: "5ms", Path: "requests/list.yaml", Env: "dev",
	}
	require.NoError(t, st.Append(rec))

	all, err := st.List()
	require.NoError(t, err)
	require.Len(t, all, 1)
	require.Equal(t, "list", all[0].Name)

	got, err := st.Get("abc")
	require.NoError(t, err)
	require.Equal(t, 200, got.Status)
	require.Equal(t, "requests/list.yaml", got.Path) // Q6: what replay re-runs
	require.Equal(t, "dev", got.Env)
}

func TestGetMissing(t *testing.T) {
	st, _ := Open(t.TempDir(), nil)
	_, err := st.Get("nope")
	require.Error(t, err)
}

// A --var override carrying a secret must not be written to disk in the clear.
func TestAppendRedactsVars(t *testing.T) {
	st, err := Open(t.TempDir(), secret.NewRedactor([]string{"s3cret"}))
	require.NoError(t, err)
	require.NoError(t, st.Append(Record{
		ID: "a", Name: "login", Path: "p.yaml",
		Vars: map[string]string{"token": "s3cret", "page": "2"},
	}))

	got, err := st.Get("a")
	require.NoError(t, err)
	require.Equal(t, "***", got.Vars["token"])
	require.Equal(t, "2", got.Vars["page"])
}

// IDs must sort chronologically, so `history list` has a meaningful order
// and `replay` targets are easy to pick out.
func TestNewIDIsTimeSortable(t *testing.T) {
	first := NewID()
	time.Sleep(2 * time.Millisecond)
	second := NewID()
	require.Less(t, first, second)
	require.NotEqual(t, first, second)
}

// A corrupt line must not make the whole history unreadable.
func TestListSkipsCorruptLines(t *testing.T) {
	dir := t.TempDir()
	st, err := Open(dir, nil)
	require.NoError(t, err)
	require.NoError(t, st.Append(Record{ID: "good", Name: "ok"}))
	require.NoError(t, appendRawLine(st, "{not json"))
	require.NoError(t, st.Append(Record{ID: "good2", Name: "ok2"}))

	all, err := st.List()
	require.NoError(t, err)
	require.Len(t, all, 2)
}

// appendRawLine writes a line to the store's file verbatim, bypassing Append.
// It simulates a corrupt or hand-edited entry in the JSONL log.
func appendRawLine(st *Store, line string) error {
	f, err := os.OpenFile(st.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	_, err = f.WriteString(line + "\n")
	return err
}
