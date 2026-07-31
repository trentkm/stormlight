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
type Context struct {
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

func normalizeContext(value Context) (Context, error) {
	value.Kind = strings.TrimSpace(strings.ToLower(value.Kind))
	value.Name = strings.TrimSpace(value.Name)
	value.ID = strings.TrimSpace(value.ID)
	if value.Kind == "" {
		return Context{}, fmt.Errorf("workspace kind is required")
	}
	if hasControl(value.Kind) || hasControl(value.Name) || hasControl(value.ID) {
		return Context{}, fmt.Errorf("workspace identity contains control characters")
	}

	root, err := canonicalDirectory(value.Root)
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
	value.ExecutionRoot, err = canonicalDirectory(value.ExecutionRoot)
	if err != nil {
		return Context{}, fmt.Errorf("execution root: %w", err)
	}

	value.ComponentName = strings.TrimSpace(value.ComponentName)
	if strings.TrimSpace(value.ComponentRoot) != "" {
		value.ComponentRoot, err = canonicalDirectory(value.ComponentRoot)
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
