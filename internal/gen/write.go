package gen

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/goccy/go-yaml"

	"github.com/RomanAgaltsev/quiver/internal/request"
)

// CollectionFileName is the collection-level file every generated tree gets.
const CollectionFileName = "collection.yaml"

// Report is what the run tells the user it did. It is documentation as much as
// output: the report is the only place a person learns that a file was skipped
// and why.
type Report struct {
	Written          []string
	SkippedEdited    []string
	SkippedUnmanaged []string
	Restored         []string
	Orphaned         []string
	Notes            []string
}

// Changed reports whether applying this plan would alter the tree — what
// --check exits 1 on.
//
// A skipped hand-edited file is deliberately not drift: skipping it is the
// promise being kept, and counting it would make CI fail forever on a file the
// user was invited to edit.
func (r *Report) Changed() bool {
	return len(r.Written) > 0 || len(r.Restored) > 0
}

// plannedFile is one file's decision, its rendered bytes, and where it goes.
type plannedFile struct {
	path   string // relative to the output directory, always slash-separated
	data   []byte
	act    action
	entry  LockEntry
	onDisk bool
}

// Plan decides what a generation would do, without touching anything.
//
// Write is Plan plus the writes, which is what lets --check run the whole
// generation in memory and compare: a --check that used a different code path
// from the real run would eventually disagree with it.
func Plan(dir string, files []GeneratedFile, c collectionFile, lock *Lock, force bool) ([]plannedFile, *Report, error) {
	rep := &Report{}

	coll, err := marshalCollection(c)
	if err != nil {
		return nil, nil, err
	}
	planned := make([]plannedFile, 0, len(files)+1)
	// The collection file is generated and lock-managed like any other: a user
	// who edits its defaults must not have them rewritten from under them.
	all := []plannedFile{{path: CollectionFileName, data: coll}}
	for _, f := range files {
		data, mErr := marshalRequest(f.Req)
		if mErr != nil {
			return nil, nil, fmt.Errorf("%s: %w", f.Path, mErr)
		}
		all = append(all, plannedFile{
			path:  filepath.ToSlash(f.Path),
			data:  data,
			entry: LockEntry{OperationID: f.OperationID},
		})
	}

	for _, p := range all {
		onDisk, exists, rErr := readIfExists(filepath.Join(dir, filepath.FromSlash(p.path)))
		if rErr != nil {
			return nil, nil, rErr
		}
		p.onDisk = exists
		p.act = decide(p.path, lock, onDisk, exists, force)
		p.entry.Hash = hashBytes(p.data)

		switch p.act {
		case actionWrite:
			// A write that would produce the bytes already there is not a change.
			// Reporting it as one would make every re-run look like work and make
			// --check useless.
			if exists && string(onDisk) == string(p.data) {
				planned = append(planned, p)
				continue
			}
			rep.Written = append(rep.Written, p.path)
		case actionRestore:
			rep.Restored = append(rep.Restored, p.path)
		case actionSkipEdited:
			rep.SkippedEdited = append(rep.SkippedEdited, p.path)
		case actionSkipUnmanaged:
			rep.SkippedUnmanaged = append(rep.SkippedUnmanaged, p.path)
		}
		planned = append(planned, p)
	}

	rep.Orphaned = orphans(lock, planned)
	return planned, rep, nil
}

// orphans are files the lock remembers but this generation did not produce:
// their operation left the spec. They are reported and left alone — deleting a
// file because an endpoint disappeared is the person's decision, not the tool's.
func orphans(lock *Lock, planned []plannedFile) []string {
	if lock == nil || len(lock.Files) == 0 {
		return nil
	}
	generated := make(map[string]bool, len(planned))
	for _, p := range planned {
		generated[p.path] = true
	}
	var out []string
	for path := range lock.Files {
		if !generated[path] {
			out = append(out, path)
		}
	}
	sort.Strings(out)
	return out
}

// Write generates the tree and returns what it did. The lockfile is written
// last and only after every file write succeeded, so a run that failed halfway
// never leaves a lock claiming files it did not write.
func Write(dir string, files []GeneratedFile, c collectionFile, lock *Lock, force bool) (*Report, error) {
	planned, rep, err := Plan(dir, files, c, lock, force)
	if err != nil {
		return nil, err
	}
	if lock.Files == nil {
		lock.Files = map[string]LockEntry{}
	}

	wrote := false
	for _, p := range planned {
		switch p.act {
		case actionWrite, actionRestore:
			full := filepath.Join(dir, filepath.FromSlash(p.path))
			if mkErr := os.MkdirAll(filepath.Dir(full), 0o755); mkErr != nil {
				return nil, fmt.Errorf("create %s: %w", filepath.Dir(p.path), mkErr)
			}
			if !p.onDisk || !sameBytes(full, p.data) {
				if wErr := os.WriteFile(full, p.data, 0o644); wErr != nil {
					return nil, fmt.Errorf("write %s: %w", p.path, wErr)
				}
				wrote = true
			}
			lock.Files[p.path] = p.entry
		case actionSkipEdited, actionSkipUnmanaged:
			// Leave both the file and, for a skipped edit, its recorded hash
			// alone: adopting the current bytes would silently make the user's
			// edits generator-owned, and the next run would overwrite them.
		}
	}

	// The comments the schema cannot carry — alternative servers, unsupported
	// security schemes — are appended after the marshalled collection, and only
	// when the collection itself was (re)written.
	if lock.Version == 0 {
		lock.Version = lockVersion
	}
	if wrote || lock.GeneratedAt.IsZero() {
		lock.GeneratedAt = time.Now().UTC().Truncate(time.Second)
	}
	if err := writeLock(dir, lock); err != nil {
		return nil, err
	}
	return rep, nil
}

func writeLock(dir string, lock *Lock) error {
	data, err := yaml.Marshal(lock)
	if err != nil {
		return fmt.Errorf("encode %s: %w", LockPath, err)
	}
	full := filepath.Join(dir, filepath.FromSlash(LockPath))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return fmt.Errorf("create %s: %w", filepath.Dir(LockPath), err)
	}
	if sameBytes(full, data) {
		return nil // nothing changed; do not touch the file's mtime either
	}
	if err := os.WriteFile(full, data, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", LockPath, err)
	}
	return nil
}

func readIfExists(path string) ([]byte, bool, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("read %s: %w", path, err)
	}
	return data, true, nil
}

func sameBytes(path string, data []byte) bool {
	cur, err := os.ReadFile(path)
	return err == nil && string(cur) == string(data)
}

// marshalRequest renders one request file.
//
// It marshals request.Request itself rather than a parallel struct, so the
// generated file can never drift from the schema `qv run` loads — and it parses
// the result straight back, because a file no executor accepts is the exact
// failure this package exists to avoid and it is cheap to rule out here.
func marshalRequest(r request.Request) ([]byte, error) {
	data, err := yaml.Marshal(r)
	if err != nil {
		return nil, fmt.Errorf("encode request: %w", err)
	}
	back, err := request.Parse(data)
	if err != nil {
		return nil, fmt.Errorf("generated request does not parse back: %w", err)
	}
	if err := back.Validate(); err != nil {
		return nil, fmt.Errorf("generated request is not valid: %w", err)
	}
	return data, nil
}

// marshalCollection renders collection.yaml, with the commented alternatives
// appended after the mapping the schema can express.
func marshalCollection(c collectionFile) ([]byte, error) {
	data, err := yaml.Marshal(c)
	if err != nil {
		return nil, fmt.Errorf("encode %s: %w", CollectionFileName, err)
	}
	if len(c.comments) == 0 {
		return data, nil
	}
	var b strings.Builder
	b.Write(data)
	b.WriteString("\n")
	for _, line := range c.comments {
		b.WriteString("# " + line + "\n")
	}
	return []byte(b.String()), nil
}
