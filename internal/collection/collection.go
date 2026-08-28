// Package collection loads a Quiver collection (a folder tree of request
// files plus a collection.yaml).
package collection

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"

	"github.com/goccy/go-yaml"

	"github.com/RomanAgaltsev/quiver/internal/request"
)

// Collection holds collection-level config loaded from collection.yaml.
type Collection struct {
	Root     string                         `yaml:"-"`
	Defaults map[string]string              `yaml:"defaults"`
	Auth     map[string]request.AuthProfile `yaml:"auth"`
	// FailOnError makes any HTTP >= 400, non-OK gRPC code or GraphQL errors
	// payload fail the run even with no assertions declared.
	FailOnError bool `yaml:"fail_on_error"`
}

// Load reads collection.yaml at root. A missing file yields an empty collection.
func Load(root string) (*Collection, error) {
	c := &Collection{
		Root:     root,
		Defaults: make(map[string]string),
		Auth:     make(map[string]request.AuthProfile),
	}
	data, err := os.ReadFile(filepath.Join(root, "collection.yaml"))
	if os.IsNotExist(err) {
		return c, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read collection.yaml: %w", err)
	}
	dec := yaml.NewDecoder(bytes.NewReader(data), yaml.DisallowUnknownField())
	if err := dec.Decode(c); err != nil {
		return nil, fmt.Errorf("parse %s: %w", filepath.Join(root, "collection.yaml"), err)
	}
	c.Root = root
	if c.Defaults == nil {
		c.Defaults = map[string]string{}
	}
	if c.Auth == nil {
		c.Auth = map[string]request.AuthProfile{}
	}
	return c, nil
}

// LoadRequest reads and validates a single request file.
func LoadRequest(path string) (*request.Request, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read request %s: %w", path, err)
	}
	r, err := request.Parse(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	// Q6: replay and history need to know where this came from; nothing else does.
	r.Path = path
	if err := r.Validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return r, nil
}

// FindRoot locates the collection root by searching upward for collection.yaml.
//
// The search is bounded: it stops at a directory containing .git, at the user's
// home directory, or at the filesystem root. The previous revision walked all the
// way to C:\ or /, so any stray collection.yaml above the working tree was
// silently adopted as the collection root (Q21).
func FindRoot(target string) (string, error) {
	dir := target
	if info, err := os.Stat(target); err == nil && !info.IsDir() {
		dir = filepath.Dir(target)
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", fmt.Errorf("resolve %s: %w", dir, err)
	}
	home, _ := os.UserHomeDir()

	for cur := abs; ; {
		if _, err := os.Stat(filepath.Join(cur, "collection.yaml")); err == nil {
			return cur, nil
		}
		// Boundaries: never escape a repository or the user's home directory.
		if _, err := os.Stat(filepath.Join(cur, ".git")); err == nil {
			break
		}
		if home != "" && cur == home {
			break
		}
		parent := filepath.Dir(cur)
		if parent == cur { // filesystem root
			break
		}
		cur = parent
	}
	return "", fmt.Errorf("no collection.yaml found at or above %s (use --collection to point at one)", dir)
}

// ListRequests loads every request at target — a single file, or every request
// file under a directory — in folder-run order.
//
// Order is `order` ascending, then path lexically; requests without `order` sort
// last. Ordering is explicit rather than filename-derived because captures chain
// along it, and because filepath.WalkDir's lexical DFS interleaves files with
// subdirectories in a way users do not expect.
func ListRequests(target string) ([]*request.Request, error) {
	info, err := os.Stat(target)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		r, err := LoadRequest(target)
		if err != nil {
			return nil, err
		}
		return []*request.Request{r}, nil
	}

	var paths []string
	walkErr := filepath.WalkDir(target, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == "environments" || d.Name() == ".qv" || d.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Name() == "collection.yaml" {
			return nil
		}
		if ext := filepath.Ext(d.Name()); ext == ".yaml" || ext == ".yml" {
			paths = append(paths, p)
		}
		return nil
	})
	if walkErr != nil {
		return nil, walkErr
	}

	reqs := make([]*request.Request, 0, len(paths))
	for _, p := range paths {
		r, err := LoadRequest(p)
		if err != nil {
			return nil, err
		}
		reqs = append(reqs, r)
	}

	sort.SliceStable(reqs, func(i, j int) bool {
		oi, oj := reqs[i].Order, reqs[j].Order
		switch {
		case oi != nil && oj != nil && *oi != *oj:
			return *oi < *oj
		case oi != nil && oj == nil:
			return true // ordered requests run before unordered ones
		case oi == nil && oj != nil:
			return false
		default:
			return reqs[i].Path < reqs[j].Path
		}
	})
	return reqs, nil
}
