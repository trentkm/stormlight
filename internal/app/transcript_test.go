package app

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/trentkm/stormlight/internal/agent"
	"github.com/trentkm/stormlight/internal/history"
	"github.com/trentkm/stormlight/internal/provider"
	"github.com/trentkm/stormlight/internal/session"
	"github.com/trentkm/stormlight/internal/workspace"
)

// readingRuntime is a runtime whose agents live somewhere this process
// cannot reach, and which can fetch a file from there.
type readingRuntime struct {
	recordingRuntime
	mu      sync.Mutex
	files   map[string]string
	capture string
	reads   int
	readErr error
}

func (r *readingRuntime) Capture(context.Context, string, int) (string, error) {
	return r.capture, nil
}

func (r *readingRuntime) ReadAgentFile(
	_ context.Context,
	_ string,
	path string,
) (io.ReadCloser, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.reads++
	if r.readErr != nil {
		return nil, r.readErr
	}
	content, ok := r.files[path]
	if !ok {
		return nil, fmt.Errorf("no such file")
	}
	return io.NopCloser(strings.NewReader(content)), nil
}

func (r *readingRuntime) readCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.reads
}

// The renderer styles as it goes, so a phrase can carry escape sequences
// through its middle. Assertions are about the words.
var ansiPattern = regexp.MustCompile(`\x1b\[[0-9;]*m`)

func plain(text string) string { return ansiPattern.ReplaceAllString(text, "") }

const transcriptJSONL = `{"type":"user","message":{"content":"why is the build red"}}
{"type":"assistant","message":{"content":[{"type":"text","text":"A missing import."}]}}
`

func serviceWithRuntime(t *testing.T, runtime session.Runtime) *Service {
	t.Helper()
	return NewServiceWithCatalog(
		runtime,
		provider.NewRegistry(),
		workspace.NewRegistry(),
		workspace.NewCatalogAt(filepath.Join(t.TempDir(), "workspaces.json")),
		history.NewLogAt(filepath.Join(t.TempDir(), "sessions.jsonl")),
	)
}

// TestRemoteTranscriptIsReadFromItsOwnMachine: the path in an agent's
// record names a file on the agent's host. Reading it here would find
// nothing — or, worse, find something.
func TestRemoteTranscriptIsReadFromItsOwnMachine(t *testing.T) {
	runtime := &readingRuntime{files: map[string]string{
		"/home/trent/.claude/projects/api/session.jsonl": transcriptJSONL,
	}}
	runtime.agents = []agent.Agent{{
		ID:             "aaa",
		Host:           "devbox",
		TranscriptPath: "/home/trent/.claude/projects/api/session.jsonl",
		Activity:       agent.ActivityIdle,
	}}
	service := serviceWithRuntime(t, runtime)

	rendered, err := service.Capture(context.Background(), "aaa", 100)
	if err != nil {
		t.Fatalf("Capture: %v", err)
	}
	for _, want := range []string{"why is the build red", "A missing import."} {
		if !strings.Contains(plain(rendered), want) {
			t.Fatalf("transcript did not cross: %q", rendered)
		}
	}
}

// TestRemoteTranscriptIsNotRefetchedEveryFrame: this runs on the
// dashboard's refresh path, and a conversation that has run all afternoon
// is not a file to pull across a tunnel several times a second.
func TestRemoteTranscriptIsNotRefetchedEveryFrame(t *testing.T) {
	runtime := &readingRuntime{files: map[string]string{"/t.jsonl": transcriptJSONL}}
	runtime.agents = []agent.Agent{{
		ID: "aaa", Host: "devbox", TranscriptPath: "/t.jsonl",
		Activity: agent.ActivityIdle,
	}}
	service := serviceWithRuntime(t, runtime)

	for range 5 {
		if _, err := service.Capture(context.Background(), "aaa", 100); err != nil {
			t.Fatalf("Capture: %v", err)
		}
	}
	if got := runtime.readCount(); got != 1 {
		t.Fatalf("fetched %d times across five refreshes, want 1", got)
	}
}

// TestALocalTranscriptIsNotCached: reading a local file is cheap enough
// that a cache would only add staleness to a conversation being watched.
func TestALocalTranscriptIsNotCached(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	if err := os.WriteFile(path, []byte(transcriptJSONL), 0o600); err != nil {
		t.Fatal(err)
	}
	runtime := &readingRuntime{files: map[string]string{}}
	runtime.agents = []agent.Agent{{
		ID: "aaa", TranscriptPath: path, Activity: agent.ActivityIdle,
	}}
	service := serviceWithRuntime(t, runtime)

	first, err := service.Capture(context.Background(), "aaa", 100)
	if err != nil || !strings.Contains(plain(first), "A missing import.") {
		t.Fatalf("Capture: %v, %q", err, first)
	}
	appended := transcriptJSONL +
		`{"type":"assistant","message":{"content":[{"type":"text","text":"Fixed now."}]}}` + "\n"
	if err := os.WriteFile(path, []byte(appended), 0o600); err != nil {
		t.Fatal(err)
	}
	second, err := service.Capture(context.Background(), "aaa", 100)
	if err != nil {
		t.Fatalf("Capture: %v", err)
	}
	if !strings.Contains(plain(second), "Fixed now.") {
		t.Fatalf("a local transcript should be read fresh: %q", second)
	}
}

// TestAnUnreadableTranscriptFallsBackToTheScreen: a host that went away
// mid-conversation should cost the reading view, not the pane.
func TestAnUnreadableTranscriptFallsBackToTheScreen(t *testing.T) {
	runtime := &readingRuntime{readErr: fmt.Errorf("ssh: connection closed")}
	runtime.agents = []agent.Agent{{
		ID: "aaa", Host: "devbox", TranscriptPath: "/t.jsonl",
		Activity: agent.ActivityIdle,
	}}
	runtime.capture = "what the terminal last showed"
	service := serviceWithRuntime(t, runtime)

	rendered, err := service.Capture(context.Background(), "aaa", 100)
	if err != nil {
		t.Fatalf("Capture: %v", err)
	}
	if !strings.Contains(plain(rendered), "what the terminal last showed") {
		t.Fatalf("want the terminal snapshot, got %q", rendered)
	}
}
