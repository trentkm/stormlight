package workspace

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
)

const catalogVersion = 1

type Catalog struct {
	path string
	mu   sync.Mutex
}

type catalogData struct {
	Version int      `json:"version"`
	Paths   []string `json:"paths"`
}

func NewCatalog() *Catalog {
	return &Catalog{path: catalogPath()}
}

func NewCatalogAt(path string) *Catalog {
	return &Catalog{path: path}
}

func (c *Catalog) Paths() ([]string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	data, err := c.read()
	if err != nil {
		return nil, err
	}
	return slices.Clone(data.Paths), nil
}

func (c *Catalog) Add(path string) error {
	canonical, err := canonicalDirectory(path)
	if err != nil {
		return err
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	data, err := c.read()
	if err != nil {
		return err
	}
	if slices.Contains(data.Paths, canonical) {
		return nil
	}
	data.Paths = append(data.Paths, canonical)
	return c.write(data)
}

func (c *Catalog) Remove(path string) error {
	canonical, err := canonicalDirectory(path)
	if err != nil {
		return err
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	data, err := c.read()
	if err != nil {
		return err
	}
	data.Paths = slices.DeleteFunc(data.Paths, func(candidate string) bool {
		return candidate == canonical
	})
	return c.write(data)
}

func (c *Catalog) read() (catalogData, error) {
	data := catalogData{Version: catalogVersion}
	if strings.TrimSpace(c.path) == "" {
		return data, nil
	}
	content, err := os.ReadFile(c.path)
	if errors.Is(err, os.ErrNotExist) {
		return data, nil
	}
	if err != nil {
		return catalogData{}, fmt.Errorf("read workspace catalog: %w", err)
	}
	if err := json.Unmarshal(content, &data); err != nil {
		return catalogData{}, fmt.Errorf("decode workspace catalog: %w", err)
	}
	if data.Version != catalogVersion {
		return catalogData{}, fmt.Errorf(
			"unsupported workspace catalog version %d",
			data.Version,
		)
	}
	return data, nil
}

func (c *Catalog) write(data catalogData) error {
	if strings.TrimSpace(c.path) == "" {
		return nil
	}
	data.Version = catalogVersion
	if err := os.MkdirAll(filepath.Dir(c.path), 0o700); err != nil {
		return fmt.Errorf("create workspace catalog directory: %w", err)
	}
	file, err := os.CreateTemp(filepath.Dir(c.path), ".workspaces-*")
	if err != nil {
		return fmt.Errorf("create workspace catalog: %w", err)
	}
	temporaryPath := file.Name()
	defer os.Remove(temporaryPath)

	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return fmt.Errorf("set workspace catalog permissions: %w", err)
	}
	encoder := json.NewEncoder(file)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(data); err != nil {
		_ = file.Close()
		return fmt.Errorf("encode workspace catalog: %w", err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return fmt.Errorf("sync workspace catalog: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close workspace catalog: %w", err)
	}
	if err := os.Rename(temporaryPath, c.path); err != nil {
		return fmt.Errorf("replace workspace catalog: %w", err)
	}
	return nil
}

func catalogPath() string {
	if configured := os.Getenv("RUNSTEAD_WORKSPACES_FILE"); configured != "" {
		return configured
	}
	if stateHome := os.Getenv("XDG_STATE_HOME"); stateHome != "" {
		return filepath.Join(stateHome, "runstead", "workspaces.json")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".local", "state", "runstead", "workspaces.json")
}
