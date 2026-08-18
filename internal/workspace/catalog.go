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
	"time"

	"github.com/trentkm/stormlight/internal/filelock"
)

const (
	// catalogVersion 2 keeps entries rather than paths: a path alone
	// stopped identifying a workspace once one could be on another
	// machine.
	catalogVersion     = 2
	catalogLockTimeout = time.Second
)

// Entry is one workspace the catalog remembers — a directory, and the
// machine it is on. An empty Host is this one.
type Entry struct {
	Host string `json:"host,omitempty"`
	Path string `json:"path"`
}

// EntryOf is the catalog's key for a resolved workspace.
func EntryOf(value Context) Entry {
	return Entry{Host: value.Host, Path: value.Root}
}

type Catalog struct {
	path string
	mu   sync.Mutex
}

type catalogData struct {
	Version int            `json:"version"`
	Entries []catalogEntry `json:"entries"`

	// Paths and Names are version 1: a list of local directories, and a
	// separate map of display names keyed by them. Read to migrate,
	// never written.
	Paths []string          `json:"paths,omitempty"`
	Names map[string]string `json:"names,omitempty"`
}

// catalogEntry folds the display name into the entry it names. In version
// 1 names lived in their own map because a path could key one; an entry
// cannot key a JSON object, and a name was never anything but a property
// of the workspace anyway.
type catalogEntry struct {
	Host string `json:"host,omitempty"`
	Path string `json:"path"`
	Name string `json:"name,omitempty"`
}

func (e catalogEntry) key() Entry { return Entry{Host: e.Host, Path: e.Path} }

func (d *catalogData) indexOf(entry Entry) int {
	return slices.IndexFunc(d.Entries, func(candidate catalogEntry) bool {
		return candidate.key() == entry
	})
}

func NewCatalog() *Catalog {
	return &Catalog{path: catalogPath()}
}

func NewCatalogAt(path string) *Catalog {
	return &Catalog{path: path}
}

// Entries reports every remembered workspace, in the order added.
func (c *Catalog) Entries() ([]Entry, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	data, err := c.read()
	if err != nil {
		return nil, err
	}
	entries := make([]Entry, 0, len(data.Entries))
	for _, stored := range data.Entries {
		entries = append(entries, stored.key())
	}
	return entries, nil
}

// canonical resolves an entry's path against the filesystem that owns it.
// For another machine that is not this one — the same rule the resolvers
// follow, and for the same reason: a path can exist on both machines and
// mean different repositories.
func (c *Catalog) canonical(entry Entry) (Entry, error) {
	entry.Host = strings.TrimSpace(entry.Host)
	if entry.Host == "" {
		path, err := canonicalDirectory(entry.Path)
		if err != nil {
			return Entry{}, err
		}
		entry.Path = path
		return entry, nil
	}
	path := strings.TrimSpace(entry.Path)
	if !filepath.IsAbs(path) {
		return Entry{}, fmt.Errorf("path %q on %s is not absolute", path, entry.Host)
	}
	entry.Path = filepath.Clean(path)
	return entry, nil
}

func (c *Catalog) Add(entry Entry) error {
	entry, err := c.canonical(entry)
	if err != nil {
		return err
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	unlock, err := c.lock()
	if err != nil {
		return err
	}
	defer unlock()

	data, err := c.read()
	if err != nil {
		return err
	}
	if data.indexOf(entry) >= 0 {
		return nil
	}
	data.Entries = append(data.Entries, catalogEntry{Host: entry.Host, Path: entry.Path})
	return c.write(data)
}

func (c *Catalog) Remove(entry Entry) error {
	entry, err := c.canonical(entry)
	if err != nil {
		return err
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	unlock, err := c.lock()
	if err != nil {
		return err
	}
	defer unlock()

	data, err := c.read()
	if err != nil {
		return err
	}
	data.Entries = slices.DeleteFunc(data.Entries, func(candidate catalogEntry) bool {
		return candidate.key() == entry
	})
	return c.write(data)
}

// SetName stores a display-name override for a workspace root. An empty
// name removes the override. The path is added to the catalog if missing so
// the name survives even for workspaces discovered through their agents.
func (c *Catalog) SetName(entry Entry, name string) error {
	entry, err := c.canonical(entry)
	if err != nil {
		return err
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	unlock, err := c.lock()
	if err != nil {
		return err
	}
	defer unlock()

	data, err := c.read()
	if err != nil {
		return err
	}
	index := data.indexOf(entry)
	if index < 0 {
		data.Entries = append(data.Entries,
			catalogEntry{Host: entry.Host, Path: entry.Path})
		index = len(data.Entries) - 1
	}
	data.Entries[index].Name = strings.TrimSpace(name)
	return c.write(data)
}

// Names returns the display-name overrides, keyed by workspace.
func (c *Catalog) Names() (map[Entry]string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	data, err := c.read()
	if err != nil {
		return nil, err
	}
	names := make(map[Entry]string)
	for _, stored := range data.Entries {
		if stored.Name != "" {
			names[stored.key()] = stored.Name
		}
	}
	return names, nil
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
	switch data.Version {
	case catalogVersion:
	case 1:
		data = migrateFromV1(data)
	default:
		return catalogData{}, fmt.Errorf(
			"unsupported workspace catalog version %d",
			data.Version,
		)
	}
	return data, nil
}

// migrateFromV1 folds a path list and its separate name map into entries.
// Everything version 1 held was on this machine, so every entry migrates
// with no host — which is exactly what an empty host means.
//
// The file is rewritten as version 2 on the next write rather than on
// read: a dashboard that only ever lists workspaces has no business
// rewriting state, and the migration is cheap enough to redo until
// something does.
func migrateFromV1(data catalogData) catalogData {
	entries := make([]catalogEntry, 0, len(data.Paths))
	for _, path := range data.Paths {
		entries = append(entries, catalogEntry{Path: path, Name: data.Names[path]})
	}
	return catalogData{Version: catalogVersion, Entries: entries}
}

func (c *Catalog) write(data catalogData) error {
	if strings.TrimSpace(c.path) == "" {
		return nil
	}
	data.Version = catalogVersion
	// Version 1's fields are read for migration and never written back,
	// or a v2 file would keep a stale copy of everything it replaced.
	data.Paths = nil
	data.Names = nil
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

// lock serializes read-modify-write mutations across Stormlight processes.
// The sibling lock file survives the catalog's atomic rename, so every
// process continues to lock the same inode.
func (c *Catalog) lock() (func(), error) {
	if strings.TrimSpace(c.path) == "" {
		return func() {}, nil
	}
	if err := os.MkdirAll(filepath.Dir(c.path), 0o700); err != nil {
		return nil, fmt.Errorf("create workspace catalog directory: %w", err)
	}
	unlock, err := filelock.Acquire(c.path+".lock", 0o600, catalogLockTimeout)
	if err != nil {
		return nil, fmt.Errorf("lock workspace catalog: %w", err)
	}
	return unlock, nil
}

func catalogPath() string {
	if configured := os.Getenv("STORMLIGHT_WORKSPACES_FILE"); configured != "" {
		return configured
	}
	if stateHome := os.Getenv("XDG_STATE_HOME"); stateHome != "" {
		return filepath.Join(stateHome, "stormlight", "workspaces.json")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(
		home,
		".local",
		"state",
		"stormlight",
		"workspaces.json",
	)
}
