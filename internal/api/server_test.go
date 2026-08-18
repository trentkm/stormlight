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
	"github.com/trentkm/stormlight/internal/pty"
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
	f.streamed = append(f.streamed, id)
	stream := f.stream
	f.mu.Unlock()
	// The stream guards its own fields; reaching past that from here is
	// how the test, not the server, grew a race.
	_ = stream.Resize(context.Background(), cols, rows)
	return stream, nil
}

// fakeStream is one attached terminal: a seed, a channel the test pushes
// output onto, and a record of what came back the other way.
type fakeStream struct {
	seed   []byte
	output chan pty.Message

	mu         sync.Mutex
	written    []byte
	cols, rows int
	// resizes records every size the terminal was asked for, because a
	// bad one that is corrected a moment later still reflowed the
	// terminal every viewer shares.
	resizes [][2]int
	closed  bool
}

func (f *fakeStream) Seed() []byte               { return f.seed }
func (f *fakeStream) Output() <-chan pty.Message { return f.output }
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
	f.resizes = append(f.resizes, [2]int{cols, rows})
	return nil
}

func (f *fakeStream) resizeHistory() [][2]int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([][2]int(nil), f.resizes...)
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
			output: make(chan pty.Message, 8),
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

// TestCrossOriginRequestsAreRefused: guarding only the upgrades leaves
// the control plane open. A cross-origin POST with a text/plain body is a
// *simple* request — no preflight, so the browser sends it and the side
// effect lands even though the reply is opaque to the attacker. Dispatch
// takes a working directory and starts a process, which is exactly the
// side effect not to hand out.
func TestCrossOriginRequestsAreRefused(t *testing.T) {
	server, runtime := startAPI(t)

	request, err := http.NewRequest(http.MethodPost,
		server.URL+"/api/agents/agent-one/message?token="+testToken,
		strings.NewReader(`{"message":"from a page you visited"}`))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Origin", "http://evil.example")
	request.Header.Set("Content-Type", "text/plain")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-origin POST answered %d, want 403", response.StatusCode)
	}
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if len(runtime.sent) != 0 {
		t.Fatalf("a cross-origin request reached the runtime: %#v", runtime.sent)
	}
}

// TestSameOriginRequestsAreAllowed is the other half of the origin
// tests, and the half that fails if the policy is merely strict: a page
// this server serves must be able to call it. Refusing everything would
// satisfy every "is it refused" test ever written.
func TestSameOriginRequestsAreAllowed(t *testing.T) {
	server, _ := startAPI(t)

	request, err := http.NewRequest(http.MethodPost,
		server.URL+"/api/agents/agent-one/message?token="+testToken,
		strings.NewReader(`{"message":"from the page you served me"}`))
	if err != nil {
		t.Fatal(err)
	}
	// What a browser sends for a page loaded from this very server.
	request.Header.Set("Origin", server.URL)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("a same-origin request answered %d, want 204", response.StatusCode)
	}

	// And a terminal socket from that same page.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	address := "ws" + strings.TrimPrefix(server.URL, "http") +
		"/api/agents/agent-one/terminal?token=" + testToken
	conn, _, err := websocket.Dial(ctx, address, &websocket.DialOptions{
		HTTPHeader: http.Header{"Origin": []string{server.URL}},
	})
	if err != nil {
		t.Fatalf("a same-origin upgrade was refused: %v", err)
	}
	conn.CloseNow()
}

// TestRebindingHostIsRefused: comparing Origin against the request's own
// Host makes the check self-referential. Under DNS rebinding both carry
// the attacker's name and match each other perfectly, so the Host has to
// be one this server actually answers to.
func TestRebindingHostIsRefused(t *testing.T) {
	server, _ := startAPI(t)

	request, err := http.NewRequest(http.MethodGet,
		server.URL+"/api/agents?token="+testToken, nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Host = "evil.example"
	request.Header.Set("Origin", "http://evil.example")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("a rebound host answered %d, want 403", response.StatusCode)
	}
}

// TestBearerHeaderParsing: the scheme is case-insensitive and its space
// is not optional, and an unrelated Authorization header — a proxy's
// Basic, a browser replaying cached credentials for 127.0.0.1 — must not
// void a perfectly good query token.
func TestBearerHeaderParsing(t *testing.T) {
	server, _ := startAPI(t)

	for _, testCase := range []struct {
		name   string
		header string
		query  bool
		want   int
	}{
		{"lowercase scheme", "bearer " + testToken, false, http.StatusOK},
		{"no space", "Bearer" + testToken, false, http.StatusUnauthorized},
		{"bare scheme", "Bearer", false, http.StatusUnauthorized},
		{"unrelated header beside a good query token",
			"Basic dXNlcjpwYXNz", true, http.StatusOK},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			url := server.URL + "/api/agents"
			if testCase.query {
				url += "?token=" + testToken
			}
			request, err := http.NewRequest(http.MethodGet, url, nil)
			if err != nil {
				t.Fatal(err)
			}
			request.Header.Set("Authorization", testCase.header)
			response, err := http.DefaultClient.Do(request)
			if err != nil {
				t.Fatalf("GET: %v", err)
			}
			defer response.Body.Close()
			if response.StatusCode != testCase.want {
				t.Fatalf("answered %d, want %d", response.StatusCode, testCase.want)
			}
		})
	}
}

// TestTerminalGeometryIsBounded: this is the one place a client's number
// reaches the daemon's emulator, which allocates the grid it is told to.
// The daemon owns every agent's process, so an unbounded size is a way to
// take down the whole fleet from one query string.
func TestTerminalGeometryIsBounded(t *testing.T) {
	server, runtime := startAPI(t)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	address := "ws" + strings.TrimPrefix(server.URL, "http") +
		"/api/agents/agent-one/terminal?token=" + testToken +
		"&cols=100000&rows=100000"
	conn, _, err := websocket.Dial(ctx, address, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.CloseNow()

	waitFor(t, "the attach to land", func() bool {
		cols, rows := runtime.stream.size()
		return cols > 0 && rows > 0
	})
	cols, rows := runtime.stream.size()
	if !usableSize(cols, rows) {
		t.Fatalf("the daemon was asked for %dx%d", cols, rows)
	}
	// Refused, not corrected: a size nobody can use must leave the
	// terminal where it was.
	if cols != 80 || rows != 24 {
		t.Fatalf("an unusable size became %dx%d instead of the default", cols, rows)
	}

	// The resize control message is the same number by another route.
	if err := conn.Write(ctx, websocket.MessageText,
		[]byte(`{"type":"resize","cols":99999,"rows":99999}`)); err != nil {
		t.Fatalf("write resize: %v", err)
	}
	// A resize the terminal cannot be is ignored, not clamped: flooring
	// it would let one message reflow the terminal every viewer shares.
	if err := conn.Write(ctx, websocket.MessageText,
		[]byte(`{"type":"resize"}`)); err != nil {
		t.Fatalf("write empty resize: %v", err)
	}
	if err := conn.Write(ctx, websocket.MessageText,
		[]byte(`{"type":"resize","cols":120,"rows":40}`)); err != nil {
		t.Fatalf("write resize: %v", err)
	}
	// The good one lands, which also proves the bad ones did not: they
	// were sent first, on one ordered socket.
	waitFor(t, "the usable resize to land", func() bool {
		cols, rows := runtime.stream.size()
		return cols == 120 && rows == 40
	})
	// The socket is ordered, so the unusable ones were handled before the
	// one that landed. None of them may have reached the terminal — a
	// size corrected to 2x2 a moment later still reflowed the pane every
	// viewer shares, which is the whole failure being prevented.
	for _, size := range runtime.stream.resizeHistory() {
		if !usableSize(size[0], size[1]) {
			t.Fatalf("the terminal was resized to %dx%d", size[0], size[1])
		}
		if size[0] == 2 || size[1] == 2 {
			t.Fatalf("an unusable resize was floored to %dx%d instead of refused",
				size[0], size[1])
		}
	}
}

// TestPlainGetDoesNotDisturbTheTerminal: attaching resizes the agent's
// terminal, and every viewer shares one. A request that cannot become a
// terminal at all must not reflow the pane a dashboard user is reading.
func TestPlainGetDoesNotDisturbTheTerminal(t *testing.T) {
	server, runtime := startAPI(t)
	runtime.stream.mu.Lock()
	runtime.stream.cols, runtime.stream.rows = 213, 57
	runtime.stream.mu.Unlock()

	response := get(t, server, "/api/agents/agent-one/terminal")
	defer response.Body.Close()

	if cols, rows := runtime.stream.size(); cols != 213 || rows != 57 {
		t.Fatalf("a plain GET resized the shared terminal to %dx%d", cols, rows)
	}
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if len(runtime.streamed) != 0 {
		t.Fatalf("a plain GET attached to %#v", runtime.streamed)
	}
}

// TestLargePasteSurvives: xterm.js sends a paste as one message, and the
// library's default read limit is 32 KiB. Exceeding it does not reject
// the message — it tears down the whole attachment, so the user loses
// both the paste and the terminal.
func TestLargePasteSurvives(t *testing.T) {
	server, runtime := startAPI(t)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	address := "ws" + strings.TrimPrefix(server.URL, "http") +
		"/api/agents/agent-one/terminal?token=" + testToken
	conn, _, err := websocket.Dial(ctx, address, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.CloseNow()
	drainSeed(t, ctx, conn)

	paste := strings.Repeat("x", 200_000)
	if err := conn.Write(ctx, websocket.MessageBinary, []byte(paste)); err != nil {
		t.Fatalf("write paste: %v", err)
	}
	waitFor(t, "the whole paste to reach the terminal", func() bool {
		return len(runtime.stream.input()) == len(paste)
	})
}

// drainSeed reads the seed notice and the state behind it.
func drainSeed(t *testing.T, ctx context.Context, conn *websocket.Conn) {
	t.Helper()
	for i := 0; i < 2; i++ {
		if _, _, err := conn.Read(ctx); err != nil {
			t.Fatalf("read seed: %v", err)
		}
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
	runtime.stream.output <- pty.Message{Bytes: []byte("hello from the pty")}
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
