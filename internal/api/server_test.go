package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/trentkm/stormlight/internal/agent"
	"github.com/trentkm/stormlight/internal/app"
	"github.com/trentkm/stormlight/internal/provider"
	"github.com/trentkm/stormlight/internal/session"
	"github.com/trentkm/stormlight/internal/workspace"
)

const testToken = "test-token"

// fakeRuntime stands in for the daemon. Embedding the interface means a
// route that reaches a method this test did not think about panics
// loudly rather than passing on a nil result.
type fakeRuntime struct {
	session.Runtime

	mu       sync.Mutex
	agents   []agent.Agent
	sent     []string
	deleted  []string
	stream   *fakeStream
	streamed []string
}

func (f *fakeRuntime) ListAgents(context.Context) ([]agent.Agent, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]agent.Agent(nil), f.agents...), nil
}

func (f *fakeRuntime) Send(_ context.Context, id, message string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, id+": "+message)
	return nil
}

func (f *fakeRuntime) Delete(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deleted = append(f.deleted, id)
	return nil
}

func (f *fakeRuntime) Update(context.Context, string, session.Update) error {
	return nil
}

func (f *fakeRuntime) SetWorkspace(context.Context, string, workspace.Context) error {
	return nil
}

func (f *fakeRuntime) AttachTerminal(
	_ context.Context,
	id string,
	cols, rows int,
) (session.TerminalStream, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.streamed = append(f.streamed, id)
	f.stream.cols, f.stream.rows = cols, rows
	return f.stream, nil
}

// fakeStream is one attached terminal: a seed, a channel the test pushes
// output onto, and a record of what came back the other way.
type fakeStream struct {
	seed   []byte
	output chan []byte

	mu         sync.Mutex
	written    []byte
	cols, rows int
	closed     bool
}

func (f *fakeStream) Seed() []byte          { return f.seed }
func (f *fakeStream) Output() <-chan []byte { return f.output }
func (f *fakeStream) Write(p []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.written = append(f.written, p...)
	return nil
}

func (f *fakeStream) Resize(_ context.Context, cols, rows int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cols, f.rows = cols, rows
	return nil
}

func (f *fakeStream) Close() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = true
}

func (f *fakeStream) input() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return string(f.written)
}

func (f *fakeStream) size() (int, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.cols, f.rows
}

func startAPI(t *testing.T) (*httptest.Server, *fakeRuntime) {
	t.Helper()
	// The service reads the workspace catalog out of XDG state, and
	// add/remove/rename write it back. Without this a test run answers
	// with — and could edit — the catalog of whoever ran it.
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	runtime := &fakeRuntime{
		agents: []agent.Agent{{
			ID:       "agent-one",
			Provider: agent.ProviderClaude,
			Name:     "cl-tests",
			Task:     "Run the tests",
			Cwd:      t.TempDir(),
			Activity: agent.ActivityWorking,
		}},
		stream: &fakeStream{
			seed:   []byte("exact state"),
			output: make(chan []byte, 8),
		},
	}
	service := app.NewService(runtime, provider.NewRegistry(), workspace.NewRegistry())
	server, err := New(service, testToken)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(server.Run())
	httpServer := httptest.NewServer(server.Handler())
	t.Cleanup(httpServer.Close)
	return httpServer, runtime
}

// get issues an authenticated request against the test server.
func get(t *testing.T, server *httptest.Server, path string) *http.Response {
	t.Helper()
	response, err := http.Get(server.URL + path + "?token=" + testToken)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	t.Cleanup(func() { response.Body.Close() })
	return response
}

func post(t *testing.T, server *httptest.Server, path, body string) *http.Response {
	t.Helper()
	response, err := http.Post(
		server.URL+path+"?token="+testToken,
		"application/json",
		strings.NewReader(body),
	)
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	t.Cleanup(func() { response.Body.Close() })
	return response
}

// TestTokenGuardsEveryRoute: this API dispatches agents and streams
// terminals across every workspace in the catalog. An unauthenticated
// mode is not a convenience worth having, so there is not one.
func TestTokenGuardsEveryRoute(t *testing.T) {
	server, _ := startAPI(t)

	for _, url := range []string{
		server.URL + "/api/agents",
		server.URL + "/api/agents?token=",
		server.URL + "/api/agents?token=not-the-token",
	} {
		response, err := http.Get(url)
		if err != nil {
			t.Fatalf("GET: %v", err)
		}
		response.Body.Close()
		if response.StatusCode != http.StatusUnauthorized {
			t.Fatalf("%s answered %d, want 401", url, response.StatusCode)
		}
	}

	// A bearer header is the other accepted form, for clients that can
	// set one.
	request, err := http.NewRequest(http.MethodGet, server.URL+"/api/agents", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+testToken)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("GET with bearer: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("bearer token answered %d, want 200", response.StatusCode)
	}
}

// TestCrossOriginUpgradeIsRefused: the token travels in a URL, and URLs
// leak. A page the user merely visited must not be able to open a
// terminal socket just because the server is on loopback.
func TestCrossOriginUpgradeIsRefused(t *testing.T) {
	server, _ := startAPI(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	address := "ws" + strings.TrimPrefix(server.URL, "http") +
		"/api/agents/agent-one/terminal?token=" + testToken
	_, _, err := websocket.Dial(ctx, address, &websocket.DialOptions{
		HTTPHeader: http.Header{"Origin": []string{"http://evil.example"}},
	})
	if err == nil {
		t.Fatal("a cross-origin upgrade was accepted")
	}
}

func TestRosterIsServed(t *testing.T) {
	server, _ := startAPI(t)

	response := get(t, server, "/api/agents")
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status %d", response.StatusCode)
	}
	var agents []agent.Agent
	if err := json.NewDecoder(response.Body).Decode(&agents); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(agents) != 1 || agents[0].ID != "agent-one" {
		t.Fatalf("roster = %#v", agents)
	}
}

// TestEmptyListsAreListsNotNull: an empty roster is what a fresh install
// looks like, and a client iterating it should not have to special-case
// the difference between "none" and "null".
func TestEmptyListsAreListsNotNull(t *testing.T) {
	server, runtime := startAPI(t)
	runtime.mu.Lock()
	runtime.agents = nil
	runtime.mu.Unlock()

	for _, path := range []string{"/api/agents", "/api/workspaces", "/api/history"} {
		response := get(t, server, path)
		if response.StatusCode != http.StatusOK {
			t.Fatalf("%s answered %d", path, response.StatusCode)
		}
		body, err := io.ReadAll(response.Body)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		if strings.TrimSpace(string(body)) != "[]" {
			t.Fatalf("%s returned %q, want []", path, strings.TrimSpace(string(body)))
		}
	}
}

// TestControlPlaneReachesTheRuntime walks the routes that act on an
// agent. Each is meant to be a decode, one Service call and an encode —
// so what this asserts is that the call actually happened, not that the
// handler did something clever on the way.
func TestControlPlaneReachesTheRuntime(t *testing.T) {
	server, runtime := startAPI(t)

	response := post(t, server, "/api/agents/agent-one/message",
		`{"message":"ship it"}`)
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("send answered %d, want 204", response.StatusCode)
	}

	request, err := http.NewRequest(http.MethodDelete,
		server.URL+"/api/agents/agent-one?token="+testToken, nil)
	if err != nil {
		t.Fatal(err)
	}
	deleted, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("DELETE: %v", err)
	}
	deleted.Body.Close()
	if deleted.StatusCode != http.StatusNoContent {
		t.Fatalf("delete answered %d, want 204", deleted.StatusCode)
	}

	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if len(runtime.sent) != 1 || runtime.sent[0] != "agent-one: ship it" {
		t.Fatalf("message did not reach the runtime: %#v", runtime.sent)
	}
	if len(runtime.deleted) != 1 || runtime.deleted[0] != "agent-one" {
		t.Fatalf("delete did not reach the runtime: %#v", runtime.deleted)
	}
}

// TestRejectsUnknownFields: a misspelled field is a client bug, and
// silently ignoring it means the caller thinks it asked for something it
// did not.
func TestRejectsUnknownFields(t *testing.T) {
	server, _ := startAPI(t)
	response := post(t, server, "/api/agents/agent-one/message",
		`{"mesage":"typo"}`)
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("status %d, want 400", response.StatusCode)
	}
}

// TestTerminalRelayCarriesBytesBothWays is the data plane's contract: the
// seed announces itself and arrives as state, live output follows as raw
// binary, and keystrokes reach the terminal unwrapped.
func TestTerminalRelayCarriesBytesBothWays(t *testing.T) {
	server, runtime := startAPI(t)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	address := "ws" + strings.TrimPrefix(server.URL, "http") +
		"/api/agents/agent-one/terminal?token=" + testToken + "&cols=120&rows=40"
	conn, _, err := websocket.Dial(ctx, address, nil)
	if err != nil {
		t.Fatalf("dial terminal: %v", err)
	}
	defer conn.CloseNow()

	// The seed is state, so it is announced before it arrives: a client
	// must replace its replica rather than append to it.
	kind, payload, err := conn.Read(ctx)
	if err != nil {
		t.Fatalf("read seed notice: %v", err)
	}
	if kind != websocket.MessageText {
		t.Fatalf("seed notice was %v, want text", kind)
	}
	var notice controlMessage
	if err := json.Unmarshal(payload, &notice); err != nil {
		t.Fatalf("decode notice: %v", err)
	}
	if notice.Type != controlSeed {
		t.Fatalf("notice type %q, want %q", notice.Type, controlSeed)
	}

	kind, payload, err = conn.Read(ctx)
	if err != nil {
		t.Fatalf("read seed: %v", err)
	}
	if kind != websocket.MessageBinary || string(payload) != "exact state" {
		t.Fatalf("seed = %v %q", kind, payload)
	}

	// Live output arrives as raw bytes, not wrapped in anything.
	runtime.stream.output <- []byte("hello from the pty")
	kind, payload, err = conn.Read(ctx)
	if err != nil {
		t.Fatalf("read output: %v", err)
	}
	if kind != websocket.MessageBinary || string(payload) != "hello from the pty" {
		t.Fatalf("output = %v %q", kind, payload)
	}

	// Input goes the other way the same way.
	if err := conn.Write(ctx, websocket.MessageBinary, []byte("typed\r")); err != nil {
		t.Fatalf("write input: %v", err)
	}
	waitFor(t, "input to reach the terminal", func() bool {
		return runtime.stream.input() == "typed\r"
	})

	// Size travels on the text channel, which is the only thing it is for.
	if err := conn.Write(ctx, websocket.MessageText,
		[]byte(`{"type":"resize","cols":100,"rows":30}`)); err != nil {
		t.Fatalf("write resize: %v", err)
	}
	waitFor(t, "resize to reach the terminal", func() bool {
		cols, rows := runtime.stream.size()
		return cols == 100 && rows == 30
	})

	runtime.mu.Lock()
	streamed := append([]string(nil), runtime.streamed...)
	runtime.mu.Unlock()
	if len(streamed) != 1 || streamed[0] != "agent-one" {
		t.Fatalf("attached to %#v", streamed)
	}
}

// TestEventStreamPushesTheRoster: a client should never poll. It gets the
// roster on connect, without waiting for something to change.
func TestEventStreamPushesTheRoster(t *testing.T) {
	server, _ := startAPI(t)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	address := "ws" + strings.TrimPrefix(server.URL, "http") +
		"/api/events?token=" + testToken
	conn, _, err := websocket.Dial(ctx, address, nil)
	if err != nil {
		t.Fatalf("dial events: %v", err)
	}
	defer conn.CloseNow()

	kind, payload, err := conn.Read(ctx)
	if err != nil {
		t.Fatalf("read event: %v", err)
	}
	if kind != websocket.MessageText {
		t.Fatalf("event was %v, want text", kind)
	}
	var event rosterEvent
	if err := json.Unmarshal(payload, &event); err != nil {
		t.Fatalf("decode event: %v", err)
	}
	if event.Type != "agents" || len(event.Agents) != 1 {
		t.Fatalf("event = %#v", event)
	}
}

func waitFor(t *testing.T, what string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
