// Package resurrect keeps a record of the agent roster on disk so it can
// outlive the tmux server that hosts it.
//
// Everything Stormlight knows about an agent lives in tmux window options,
// next to the process the options describe. That is the right home while the
// server is up and the wrong one the instant it is not: a reboot, a
// kill-server, or an OOM takes the whole roster with it — not just the
// processes, which were always going to die, but the record of which
// workspace each agent was in, what it was asked to do, and which of them was
// waiting on an answer.
//
// The providers keep their side. A Claude conversation is still sitting in
// its transcript file. So the loss is Stormlight's alone to fix, and this
// package is the fix: a snapshot mirroring the roster, captured continuously
// because a server death gives no warning.
package resurrect

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

	"github.com/trentkm/stormlight/internal/agent"
	"github.com/trentkm/stormlight/internal/workspace"
)

const snapshotVersion = 1

// Entry is one agent as the snapshot remembers it: everything the runtime
// would have to be told to build the window back, plus the state that makes
// the restored row read the way the lost one did.
//
// Live process state is deliberately absent. Activity, exit codes, and pane
// ids describe a process that is by definition gone by the time anyone reads
// this file; recording them would only invite a restore that claims to know
// something it cannot.
type Entry struct {
	ID          string               `json:"id"`
	Provider    agent.Provider       `json:"provider"`
	Name        string               `json:"name"`
	Task        string               `json:"task"`
	Summary     string               `json:"summary,omitempty"`
	Cwd         string               `json:"cwd"`
	CreatedAt   time.Time            `json:"created_at,omitzero"`
	Mode        agent.PermissionMode `json:"mode,omitempty"`
	Attention   agent.Attention      `json:"attention,omitempty"`
	AttentionAt time.Time            `json:"attention_at,omitzero"`
	Mark        agent.Mark           `json:"mark,omitempty"`
	// SessionID is the provider's own id for the conversation — the value
	// its resume command takes — reported by its lifecycle hooks. It is
	// the primary handle for bringing the conversation back.
	SessionID string `json:"session_id,omitempty"`
	// TranscriptPath is the provider's own conversation file. It is the
	// fallback handle — an entry recorded before session ids were captured
	// still names its conversation through the transcript's filename — and
	// the existence check that says whether the provider still holds the
	// conversation at all.
	TranscriptPath string            `json:"transcript_path,omitempty"`
	Workspace      workspace.Context `json:"workspace"`
}

// Snapshot is the whole roster at one moment, plus the session it belonged
// to — restoring into a differently-named session is a different world, and
// the file says which one it came from rather than assuming.
type Snapshot struct {
	Version int       `json:"version"`
	Session string    `json:"session,omitempty"`
	SavedAt time.Time `json:"saved_at"`
	Agents  []Entry   `json:"agents"`
}

// Find returns the entry with the given id, or a prefix of it, matching how
// every other Stormlight command accepts a shortened agent id.
func (s Snapshot) Find(id string) (Entry, bool) {
	for _, entry := range s.Agents {
		if entry.ID == id {
			return entry, true
		}
	}
	var match Entry
	found := false
	for _, entry := range s.Agents {
		if !strings.HasPrefix(entry.ID, id) {
			continue
		}
		if found {
			return Entry{}, false
		}
		match, found = entry, true
	}
	return match, found
}

// EntryOf reduces a live agent to what survives it.
func EntryOf(live agent.Agent) Entry {
	return Entry{
		ID:             live.ID,
		Provider:       live.Provider,
		Name:           live.Name,
		Task:           live.Task,
		Summary:        live.Summary,
		Cwd:            live.Cwd,
		CreatedAt:      live.CreatedAt,
		Mode:           live.Mode,
		Attention:      live.Attention,
		AttentionAt:    live.AttentionAt,
		Mark:           live.Mark,
		SessionID:      live.SessionID,
		TranscriptPath: live.TranscriptPath,
		Workspace:      live.Workspace,
	}
}

// Store is the snapshot file. Writes are atomic — a temporary file renamed
// over the target — because the thing most likely to interrupt one is
// exactly the event the file exists for.
type Store struct {
	path string
	mu   sync.Mutex

	// fingerprint is the last roster written. A dashboard polls the runtime
	// about once a second and the roster changes far less often than that, so
	// the common case is recognising that there is nothing to write.
	fingerprint string
}

func NewStore() *Store {
	return &Store{path: SnapshotPath()}
}

func NewStoreAt(path string) *Store {
	return &Store{path: path}
}

// Path is where this store writes, for commands that want to name the file.
func (s *Store) Path() string {
	return s.path
}

func (s *Store) Load() (Snapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.read()
}

// Save replaces the snapshot with the given roster.
//
// It is only ever called with an authoritative listing. "No agents" and "no
// tmux server" arrive as the same empty slice, and writing the second as the
// first would erase the snapshot at precisely the moment it is needed; the
// caller settles that question before calling, because only the runtime can.
func (s *Store) Save(sessionName string, agents []agent.Agent) error {
	entries := make([]Entry, 0, len(agents))
	for _, live := range agents {
		entries = append(entries, EntryOf(live))
	}
	// A total order, because tmux stamps creation to the second: two agents
	// dispatched together would otherwise swap places between saves and make
	// every listing look like a change.
	slices.SortFunc(entries, func(a, b Entry) int {
		if order := a.CreatedAt.Compare(b.CreatedAt); order != 0 {
			return order
		}
		return strings.Compare(a.ID, b.ID)
	})

	s.mu.Lock()
	defer s.mu.Unlock()

	print := fingerprint(sessionName, entries)
	if print == s.fingerprint {
		return nil
	}
	if err := s.write(Snapshot{
		Session: sessionName,
		SavedAt: time.Now().UTC(),
		Agents:  entries,
	}); err != nil {
		return err
	}
	s.fingerprint = print
	return nil
}

// Forget drops one agent from the snapshot. Deleting an agent is the human
// saying they are done with it, and a roster listing cannot distinguish that
// from a window that vanished with its server — so the deletion says so
// itself, here, rather than being inferred later.
func (s *Store) Forget(ids ...string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := s.read()
	if err != nil {
		return err
	}
	before := len(data.Agents)
	data.Agents = slices.DeleteFunc(data.Agents, func(entry Entry) bool {
		return slices.Contains(ids, entry.ID)
	})
	if len(data.Agents) == before {
		return nil
	}
	data.SavedAt = time.Now().UTC()
	if err := s.write(data); err != nil {
		return err
	}
	s.fingerprint = fingerprint(data.Session, data.Agents)
	return nil
}

func (s *Store) read() (Snapshot, error) {
	data := Snapshot{Version: snapshotVersion}
	if strings.TrimSpace(s.path) == "" {
		return data, nil
	}
	content, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return data, nil
	}
	if err != nil {
		return Snapshot{}, fmt.Errorf("read agent snapshot: %w", err)
	}
	if err := json.Unmarshal(content, &data); err != nil {
		return Snapshot{}, fmt.Errorf("decode agent snapshot: %w", err)
	}
	if data.Version != snapshotVersion {
		return Snapshot{}, fmt.Errorf(
			"unsupported agent snapshot version %d",
			data.Version,
		)
	}
	return data, nil
}

func (s *Store) write(data Snapshot) error {
	if strings.TrimSpace(s.path) == "" {
		return nil
	}
	data.Version = snapshotVersion
	if data.Agents == nil {
		data.Agents = []Entry{}
	}
	directory := filepath.Dir(s.path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return fmt.Errorf("create agent snapshot directory: %w", err)
	}
	file, err := os.CreateTemp(directory, ".session-*")
	if err != nil {
		return fmt.Errorf("create agent snapshot: %w", err)
	}
	temporaryPath := file.Name()
	defer os.Remove(temporaryPath)

	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return fmt.Errorf("set agent snapshot permissions: %w", err)
	}
	encoder := json.NewEncoder(file)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(data); err != nil {
		_ = file.Close()
		return fmt.Errorf("encode agent snapshot: %w", err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return fmt.Errorf("sync agent snapshot: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close agent snapshot: %w", err)
	}
	if err := os.Rename(temporaryPath, s.path); err != nil {
		return fmt.Errorf("replace agent snapshot: %w", err)
	}
	return nil
}

// fingerprint covers the fields a restore reads back. Anything outside it
// changing is not a reason to rewrite the file — and the timestamp the write
// stamps is deliberately outside it, or every save would look like a change.
func fingerprint(sessionName string, entries []Entry) string {
	var builder strings.Builder
	builder.WriteString(sessionName)
	for _, entry := range entries {
		builder.WriteString("\x1f")
		builder.WriteString(strings.Join([]string{
			entry.ID,
			string(entry.Provider),
			entry.Name,
			entry.Task,
			entry.Summary,
			entry.Cwd,
			string(entry.Mode),
			string(entry.Attention),
			string(entry.Mark),
			entry.SessionID,
			entry.TranscriptPath,
			entry.Workspace.ID,
			entry.Workspace.ExecutionRoot,
		}, "\x1e"))
	}
	return builder.String()
}

// SnapshotPath sits beside the workspace catalog in XDG state, and answers
// to an override of its own for tests and for running more than one
// Stormlight against one machine.
func SnapshotPath() string {
	if configured := os.Getenv("STORMLIGHT_SNAPSHOT_FILE"); configured != "" {
		return configured
	}
	if stateHome := os.Getenv("XDG_STATE_HOME"); stateHome != "" {
		return filepath.Join(stateHome, "stormlight", "session.json")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".local", "state", "stormlight", "session.json")
}
