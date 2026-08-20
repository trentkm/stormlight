package workspace

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode"
)

const (
	KindDirectory = "directory"
	KindGit       = "git"
)

// Context describes the workspace grouping and execution boundary for an agent.
//
// Every path on it is canonical by the time anyone reads one. normalize
// puts Root, ExecutionRoot and ComponentRoot through a single canonical
// form on the way in — symlinks resolved against the filesystem that owns
// them for a local workspace, cleaned for a remote one, where no local
// stat could say anything true. That is what makes them comparable with
// ==, and it is the reason no consumer needs to resolve them again: doing
// so downstream would be redundant work on the local path and an answer
// about the wrong machine on the remote one.
type Context struct {
	// Host names the machine this workspace is on; empty means this one.
	// It belongs to identity rather than decoration: two machines can
	// hold the same path, and those are two workspaces, not one.
	Host          string            `json:"host,omitempty"`
	ID            string            `json:"id"`
	Kind          string            `json:"kind"`
	Name          string            `json:"name"`
	Root          string            `json:"root"`
	ExecutionRoot string            `json:"execution_root"`
	ComponentName string            `json:"component_name,omitempty"`
	ComponentRoot string            `json:"component_root,omitempty"`
	Metadata      map[string]string `json:"metadata,omitempty"`
}

func (c Context) IsZero() bool {
	return c.ID == ""
}

// OnHost is a context resolved on another machine, as this dashboard has
// to record it. The resolver over there answers about its own filesystem
// and has no idea what this side calls it, so the host is stamped here —
// and the ID is qualified with it, because /home/me/src/api on two
// machines is two workspaces and an unqualified ID would merge them into
// one group holding agents that cannot see each other's files.
func (c Context) OnHost(host string) Context {
	if host == "" || c.Host == host {
		return c
	}
	c.Host = host
	c.ID = host + ":" + c.ID
	return c
}

// Tail is the lineage segment that follows the workspace name: a
// resolver-supplied component, or the worktree directory when the agent runs
// outside the main checkout. It is empty when neither says anything the
// workspace name does not already say. The dashboard's workspace subtitle and
// other surfaces read it too, so the rule lives here
// rather than in either renderer.
func (c Context) Tail() string {
	tail := c.ComponentName
	if tail == "" && c.ExecutionRoot != "" && c.ExecutionRoot != c.Root {
		tail = pathName(c.ExecutionRoot)
	}
	if tail == c.Name {
		return ""
	}
	return tail
}

func (c Context) IsComplete() bool {
	return c.ID != "" &&
		c.Kind != "" &&
		c.Name != "" &&
		c.Root != "" &&
		c.ExecutionRoot != ""
}

func DirectoryContext(path string) Context {
	path = filepath.Clean(path)
	return Context{
		ID:            KindDirectory + ":" + path,
		Kind:          KindDirectory,
		Name:          pathName(path),
		Root:          path,
		ExecutionRoot: path,
	}
}

func canonicalDirectory(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		var err error
		path, err = os.Getwd()
		if err != nil {
			return "", fmt.Errorf("get current directory: %w", err)
		}
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve directory %q: %w", path, err)
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", fmt.Errorf("resolve directory %q: %w", absolute, err)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", fmt.Errorf("inspect directory %q: %w", resolved, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%s is not a directory", resolved)
	}
	return filepath.Clean(resolved), nil
}

// normalizeContext is the local form: every path is checked against this
// machine's filesystem, because on this machine that is knowable.
func normalizeContext(value Context) (Context, error) {
	return normalize(value, canonicalDirectory)
}

// normalizeRemoteContext is the form for a context that was resolved on
// another machine. The identity rules are the same; the path rules cannot
// be, because the only filesystem this process can stat is the wrong one.
// A local check here would be worse than none: it would fail for a path
// that exists over there, and quietly pass for one that happens to exist
// here too.
func normalizeRemoteContext(value Context) (Context, error) {
	return normalize(value, func(path string) (string, error) {
		path = strings.TrimSpace(path)
		if path == "" {
			return "", fmt.Errorf("a remote path cannot be empty")
		}
		if !filepath.IsAbs(path) {
			return "", fmt.Errorf("remote path %q is not absolute", path)
		}
		return filepath.Clean(path), nil
	})
}

func normalize(value Context, canonical func(string) (string, error)) (Context, error) {
	value.Host = strings.TrimSpace(value.Host)
	value.Kind = strings.TrimSpace(strings.ToLower(value.Kind))
	value.Name = strings.TrimSpace(value.Name)
	value.ID = strings.TrimSpace(value.ID)
	if value.Kind == "" {
		return Context{}, fmt.Errorf("workspace kind is required")
	}
	if hasControl(value.Kind) || hasControl(value.Name) || hasControl(value.ID) ||
		hasControl(value.Host) {
		return Context{}, fmt.Errorf("workspace identity contains control characters")
	}

	root, err := canonical(value.Root)
	if err != nil {
		return Context{}, fmt.Errorf("workspace root: %w", err)
	}
	value.Root = root
	if value.Name == "" {
		value.Name = pathName(root)
	}
	if value.ID == "" {
		value.ID = value.Kind + ":" + root
	}

	if strings.TrimSpace(value.ExecutionRoot) == "" {
		value.ExecutionRoot = root
	}
	value.ExecutionRoot, err = canonical(value.ExecutionRoot)
	if err != nil {
		return Context{}, fmt.Errorf("execution root: %w", err)
	}

	value.ComponentName = strings.TrimSpace(value.ComponentName)
	if strings.TrimSpace(value.ComponentRoot) != "" {
		value.ComponentRoot, err = canonical(value.ComponentRoot)
		if err != nil {
			return Context{}, fmt.Errorf("component root: %w", err)
		}
		if value.ComponentName == "" {
			value.ComponentName = pathName(value.ComponentRoot)
		}
	}
	if hasControl(value.ComponentName) {
		return Context{}, fmt.Errorf("component name contains control characters")
	}

	if len(value.Metadata) > 0 {
		metadata := make(map[string]string, len(value.Metadata))
		for key, item := range value.Metadata {
			key = strings.TrimSpace(key)
			if key == "" || hasControl(key) {
				return Context{}, fmt.Errorf("workspace metadata key is invalid")
			}
			item = strings.Map(func(value rune) rune {
				if unicode.IsControl(value) {
					return ' '
				}
				return value
			}, item)
			metadata[key] = strings.Join(strings.Fields(item), " ")
		}
		value.Metadata = metadata
	}
	return value, nil
}

func pathName(path string) string {
	name := filepath.Base(filepath.Clean(path))
	if name == "." || name == string(filepath.Separator) {
		return path
	}
	return name
}

func hasControl(value string) bool {
	return strings.IndexFunc(value, unicode.IsControl) >= 0
}
