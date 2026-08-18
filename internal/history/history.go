// Package history keeps the on-disk record of every provider conversation
// Stormlight has run. Live agent metadata lives in windrunner session
// metadata and dies with the session; the provider's conversation outlives
// both — its transcript stays on disk and its session id still resumes — so
// the record that ties id, task, and workspace together has to live
// somewhere a session
// close cannot reach.
//
// The log is append-only JSONL, one full record per write, coalesced by
// session id on read with the last record winning. Appends come from hook
// processes that run concurrently — one per provider event, across every
// live agent — which is why this is not a read-modify-write catalog like
// workspaces.json: an O_APPEND write of one line needs no coordination with
// its peers, where racing rewrites of a shared object would eat each
// other's records. The redundancy of re-writing the whole record every
// event is the price of that, and Compact reclaims it.
package history

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/trentkm/stormlight/internal/agent"
	"github.com/trentkm/stormlight/internal/filelock"
	"github.com/trentkm/stormlight/internal/workspace"
)

// Record is one provider conversation as last seen. Every append carries
// the full record rather than a delta, so a reader needs only the newest
// line per session id and a torn or lost line costs one update, not the
// integrity of the log.
type Record struct {
	// SessionID is the provider's own id for the conversation — the value
	// `claude --resume` and `codex resume` take — and the log's key.
	SessionID string               `json:"session_id"`
	Provider  agent.Provider       `json:"provider"`
	AgentID   string               `json:"agent_id,omitempty"`
	Name      string               `json:"name,omitempty"`
	Task      string               `json:"task,omitempty"`
	Summary   string               `json:"summary,omitempty"`
	Cwd       string               `json:"cwd,omitempty"`
	Mode      agent.PermissionMode `json:"mode,omitempty"`
	// TranscriptPath points at the provider's own transcript file. The log
	// stores the pointer, never a copy, so providers that prune old
	// sessions leave records whose transcript is gone; HasTranscript is
	// the liveness check.
	TranscriptPath string            `json:"transcript_path,omitempty"`
	Workspace      workspace.Context `json:"workspace,omitempty"`
	CreatedAt      time.Time         `json:"created_at"`
	UpdatedAt      time.Time         `json:"updated_at"`
}

// HasTranscript reports whether the provider's transcript file still
// exists. A record whose transcript the provider pruned is still history —
// it names what ran and when — but there is nothing left to render.
func (r Record) HasTranscript() bool {
	if r.TranscriptPath == "" {
		return false
	}
	_, err := os.Stat(r.TranscriptPath)
	return err == nil
}

func (r Record) DisplaySummary() string {
	if strings.TrimSpace(r.Summary) != "" {
		return strings.TrimSpace(r.Summary)
	}
	return strings.TrimSpace(r.Task)
}

// Log is a handle on the session history file. The zero value is unusable;
// NewLog resolves the real path and NewLogAt serves tests.
type Log struct {
	path string
}

func NewLog() *Log {
	return &Log{path: logPath()}
}

func NewLogAt(path string) *Log {
	return &Log{path: path}
}

func (l *Log) Path() string {
	return l.path
}

// Append records the current state of one conversation. Concurrent appends
// are expected — hook processes across agents fire independently — and are
// safe: the line lands with a single O_APPEND write under the log's lock.
func (l *Log) Append(record Record) error {
	if l.path == "" {
		return fmt.Errorf("session history path is not resolvable")
	}
	if record.SessionID == "" {
		return fmt.Errorf("session history record has no session id")
	}
	encoded, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("encode session history record: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(l.path), 0o755); err != nil {
		return fmt.Errorf("create session history directory: %w", err)
	}
	unlock, err := l.lock()
	if err != nil {
		return err
	}
	defer unlock()
	file, err := os.OpenFile(l.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("open session history: %w", err)
	}
	defer file.Close()
	if _, err := file.Write(append(encoded, '\n')); err != nil {
		return fmt.Errorf("append session history: %w", err)
	}
	return nil
}

// Records returns one record per session, newest first. Lines that fail to
// decode are skipped rather than failing the read: a torn write from a
// killed hook process costs that one update, and the session's next event
// re-writes the whole record anyway.
func (l *Log) Records() ([]Record, error) {
	records, _, err := l.load()
	return records, err
}

// Decode reads a log's contents the way Records does — newest first, one
// record per session, torn lines skipped — without owning the file. It is
// how a dashboard reads the log of a machine it is not on: that machine
// hands over its own, and the rules for making sense of it are the same
// everywhere.
func Decode(data []byte) []Record {
	records, _ := decode(data)
	return records
}

func (l *Log) load() ([]Record, int, error) {
	if l.path == "" {
		return nil, 0, fmt.Errorf("session history path is not resolvable")
	}
	data, err := os.ReadFile(l.path)
	if os.IsNotExist(err) {
		return nil, 0, nil
	}
	if err != nil {
		return nil, 0, fmt.Errorf("read session history: %w", err)
	}
	records, lines := decode(data)
	return records, lines, nil
}

func decode(data []byte) ([]Record, int) {
	lines := 0
	byID := map[string]int{}
	var records []Record
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		lines++
		var record Record
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			continue
		}
		if record.SessionID == "" {
			continue
		}
		if index, seen := byID[record.SessionID]; seen {
			records[index] = record
			continue
		}
		byID[record.SessionID] = len(records)
		records = append(records, record)
	}
	slices.SortStableFunc(records, func(a, b Record) int {
		return b.UpdatedAt.Compare(a.UpdatedAt)
	})
	return records, lines
}

// compactionSlack is how many redundant lines the log tolerates before
// Compact rewrites it. Every event re-appends its session's whole record,
// so the file grows past its information content by design; below this
// floor a rewrite saves less than it costs.
const compactionSlack = 256
const lockTimeout = 5 * time.Second

// Compact rewrites the log as one line per session when enough redundant
// lines have accumulated. It is meant for a startup path — the dashboard
// runs it once — not for the event path, whose whole point is to never
// contend on a rewrite.
func (l *Log) Compact() error {
	unlock, err := l.lock()
	if err != nil {
		return err
	}
	defer unlock()
	records, lines, err := l.load()
	if err != nil {
		return err
	}
	if lines <= len(records)+compactionSlack {
		return nil
	}
	var encoded []byte
	// Oldest first, so a future append-then-read still sees newest last.
	for index := len(records) - 1; index >= 0; index-- {
		line, err := json.Marshal(records[index])
		if err != nil {
			return fmt.Errorf("encode session history record: %w", err)
		}
		encoded = append(encoded, line...)
		encoded = append(encoded, '\n')
	}
	temporary := l.path + ".compact"
	if err := os.WriteFile(temporary, encoded, 0o644); err != nil {
		return fmt.Errorf("write compacted session history: %w", err)
	}
	if err := os.Rename(temporary, l.path); err != nil {
		return fmt.Errorf("replace session history: %w", err)
	}
	return nil
}

// lock serializes writers on a sibling lock file. The lock file, not the
// log itself, carries the flock: Compact replaces the log by rename, and a
// writer that locked the old inode would append into the orphaned file. The
// lock file's path never changes, so its inode is the one thing every
// writer agrees on.
func (l *Log) lock() (func(), error) {
	unlock, err := filelock.Acquire(l.path+".lock", 0o644, lockTimeout)
	if err != nil {
		return nil, fmt.Errorf("lock session history: %w", err)
	}
	return unlock, nil
}

func logPath() string {
	if configured := os.Getenv("STORMLIGHT_SESSIONS_FILE"); configured != "" {
		return configured
	}
	if stateHome := os.Getenv("XDG_STATE_HOME"); stateHome != "" {
		return filepath.Join(stateHome, "stormlight", "sessions.jsonl")
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
		"sessions.jsonl",
	)
}
