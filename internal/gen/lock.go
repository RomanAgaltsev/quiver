package gen

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/goccy/go-yaml"
)

// LockPath is where the lockfile lives inside the output directory.
//
// It shares the .qv/ prefix with .qv/history/, which is gitignored — and this
// one is the opposite case. The lockfile is what makes a teammate's re-run
// behave like yours, so it belongs in the repository; the README says so
// because the shared prefix makes the wrong assumption the natural one.
const LockPath = ".qv/gen.lock"

// lockVersion is the lockfile format version. A future format change reads this
// and can refuse rather than misinterpret.
const lockVersion = 1

// Lock records what the generator wrote, so a re-run can tell a file it owns
// from one a person has since made their own.
type Lock struct {
	Version int    `yaml:"version"`
	Source  string `yaml:"source"`
	// SourceHash is omitted when empty: a --reflect source is a live server with
	// no stable bytes to hash, and inventing one would make every server restart
	// look like spec drift.
	SourceHash  string               `yaml:"source_hash,omitempty"`
	Generator   string               `yaml:"generator"`
	GeneratedAt time.Time            `yaml:"generated_at"`
	Files       map[string]LockEntry `yaml:"files"`
}

// LockEntry is one generated file: what produced it, and what it looked like
// when the generator last wrote it.
type LockEntry struct {
	OperationID string `yaml:"operation_id,omitempty"`
	Hash        string `yaml:"hash"`
}

// newLock returns an empty lock for a source spec. GeneratedAt is left zero on
// purpose: Write stamps it, and only when it actually wrote something, so a
// re-run that changes nothing leaves the lockfile byte-identical rather than
// putting a fresh timestamp into everyone's diff.
func newLock(source, sourceHash string) *Lock {
	return &Lock{
		Version:    lockVersion,
		Source:     source,
		SourceHash: sourceHash,
		Files:      map[string]LockEntry{},
	}
}

// LoadLock reads the lockfile under dir. A missing one is not an error: it is
// the first run, and every file on disk is then unmanaged.
func LoadLock(dir string) (*Lock, error) {
	data, err := os.ReadFile(filepath.Join(dir, LockPath))
	if errors.Is(err, fs.ErrNotExist) {
		return newLock("", ""), nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", LockPath, err)
	}
	var l Lock
	if err := yaml.Unmarshal(data, &l); err != nil {
		return nil, fmt.Errorf("parse %s: %w", LockPath, err)
	}
	if l.Version > lockVersion {
		return nil, fmt.Errorf(
			"%s was written by a newer qv (lock version %d, this build understands %d)",
			LockPath, l.Version, lockVersion)
	}
	if l.Files == nil {
		l.Files = map[string]LockEntry{}
	}
	return &l, nil
}

// hashBytes fingerprints a file's exact contents.
//
// It hashes the bytes as written, with no normalisation and no re-marshalling:
// a re-read of an untouched file has to match exactly, or every re-run would
// report every file as hand-edited.
func hashBytes(b []byte) string {
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// action is what a re-run does with one file.
type action int

const (
	actionWrite action = iota
	actionSkipEdited
	actionSkipUnmanaged
	actionRestore
)

// decide implements the re-generation table: what to do about one file given
// what the lock remembers and what is actually on disk.
//
// There is no three-way merge here on purpose. Merging YAML a person has
// restructured cannot be done correctly without a model of their intent, and a
// merge that is usually right is the worst option available: it corrupts
// quietly, in the files that are their source of truth. Skipping is always
// correct and always visible.
func decide(path string, lock *Lock, onDisk []byte, exists, force bool) action {
	var entry LockEntry
	managed := false
	if lock != nil && lock.Files != nil {
		entry, managed = lock.Files[path]
	}

	switch {
	case !managed && !exists:
		return actionWrite // a new operation
	case !managed && exists:
		if force {
			return actionWrite
		}
		return actionSkipUnmanaged // never written by the generator; not its to touch
	case managed && !exists:
		return actionRestore // deleted since; put it back and say so
	case entry.Hash == hashBytes(onDisk):
		return actionWrite // untouched since generation: still generator-owned
	case force:
		return actionWrite
	default:
		return actionSkipEdited
	}
}

// String names an action for the report and for test failures.
func (a action) String() string {
	switch a {
	case actionWrite:
		return "write"
	case actionSkipEdited:
		return "skip (hand-edited)"
	case actionSkipUnmanaged:
		return "skip (unmanaged)"
	case actionRestore:
		return "restore"
	default:
		return "unknown"
	}
}

// SetSource records which document a lock was generated from. The hash is what
// lets a later run notice the spec itself changed, independently of whether any
// output did.
func (l *Lock) SetSource(path string, spec []byte) {
	l.Source = path
	l.SourceHash = hashBytes(spec)
}

// SetReflectSource records a live reflective server as the source.
//
// No hash is recorded, deliberately: a server has no stable bytes to fingerprint
// and inventing one — hashing the enumerated method list, say — would make every
// deployment that reorders services look like drift. Drift detection is
// therefore weaker for a reflection source, which the README says out loud.
func (l *Lock) SetReflectSource(target string) {
	l.Source = "reflect://" + target
	l.SourceHash = ""
}
