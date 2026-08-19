package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
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
	// Mirrors windrun.AttachTerminal: a caller with no size to assert
	// must not move a terminal every viewer shares. The stream guards its
	// own fields, so this goes through Resize rather than reaching past
	// it — which is how the test, not the server, grew a race.
	if cols >= 2 && rows >= 2 {
		_ = stream.Resize(context.Background(), cols, rows)
	}
	return stream, nil
}

// fakeStream is one attached terminal: a seed, a channel the test pushes
// output onto, and a record of what came back the other way.
type fakeStream struct {
	seed     []byte
	seedSize *pty.Size
	output   chan pty.Message

	mu         sync.Mutex
	written    []byte
	cols, rows int
	// resizes records every size the terminal was asked for, because a
	// bad one that is corrected a moment later still reflowed the
	// terminal every viewer shares.
	resizes [][2]int
	closed  bool
}

func (f *fakeStream) Seed() pty.Message {
	return pty.Message{Resync: f.seed, Resize: f.seedSize}
}
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
	server, err := New(service, testToken, fstest.MapFS{
		"index.html":    &fstest.MapFile{Data: []byte("<title>Stormlight</title>")},
		"assets/app.js": &fstest.MapFile{Data: []byte("console.log('app')")},
	})
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
//
// A size the terminal cannot be is not corrected to a default either. The
// terminal is shared, so a default is an opinion asserted on every other
// viewer — and nothing puts back the size a dashboard pane had (#155).
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

	// Nothing is asserted at all: the fake starts at zero, and an attach
	// that named an impossible size leaves it there.
	waitFor(t, "the attach to land", func() bool {
		runtime.mu.Lock()
		defer runtime.mu.Unlock()
		return len(runtime.streamed) > 0
	})
	if cols, rows := runtime.stream.size(); cols != 0 || rows != 0 {
		t.Fatalf("an impossible size moved the shared terminal to %dx%d", cols, rows)
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

// A client that named no size of its own — because its pane had not been
// laid out, and a shared terminal is not a thing to reflow on a guess —
// has no other way to learn the geometry its seed was wrapped for. The
// size leads the state it belongs to.
func TestTheSeedCarriesTheSizeItWasRenderedAt(t *testing.T) {
	server, runtime := startAPI(t)
	runtime.stream.seedSize = &pty.Size{Cols: 132, Rows: 43}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	address := "ws" + strings.TrimPrefix(server.URL, "http") +
		"/api/agents/agent-one/terminal?token=" + testToken
	conn, _, err := websocket.Dial(ctx, address, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.CloseNow()

	if notice := readControl(t, ctx, conn); notice.Type != controlResize ||
		notice.Cols != 132 || notice.Rows != 43 {
		t.Fatalf("first message = %#v, want the seed's 132x43", notice)
	}
	if notice := readControl(t, ctx, conn); notice.Type != controlSeed {
		t.Fatalf("second message = %#v, want a seed notice", notice)
	}
}

// TestResyncReachesTheBrowserAsState: a viewer that falls behind is sent
// the terminal as it now stands instead of the bytes it missed, and the
// browser has to be told to replace its replica rather than append. It is
// the same seed notice the attach sends, preceded by the size the state
// was rendered at — the daemon sends a viewer in debt nothing else, so
// that is the only place it learns the terminal moved.
func TestResyncReachesTheBrowserAsState(t *testing.T) {
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

	runtime.stream.output <- pty.Message{
		Resync: []byte("exact state after falling behind"),
		Resize: &pty.Size{Cols: 132, Rows: 43},
	}

	if notice := readControl(t, ctx, conn); notice.Type != controlResize ||
		notice.Cols != 132 || notice.Rows != 43 {
		t.Fatalf("first message = %#v, want a 132x43 resize", notice)
	}
	if notice := readControl(t, ctx, conn); notice.Type != controlSeed {
		t.Fatalf("second message = %#v, want a seed notice", notice)
	}
	kind, payload, err := conn.Read(ctx)
	if err != nil {
		t.Fatalf("read state: %v", err)
	}
	if kind != websocket.MessageBinary ||
		string(payload) != "exact state after falling behind" {
		t.Fatalf("state = %v %q", kind, payload)
	}

	// And the stream carries on from there.
	runtime.stream.output <- pty.Message{Bytes: []byte("and onward")}
	kind, payload, err = conn.Read(ctx)
	if err != nil {
		t.Fatalf("read output: %v", err)
	}
	if kind != websocket.MessageBinary || string(payload) != "and onward" {
		t.Fatalf("output after resync = %v %q", kind, payload)
	}
}

func readControl(
	t *testing.T,
	ctx context.Context,
	conn *websocket.Conn,
) controlMessage {
	t.Helper()
	kind, payload, err := conn.Read(ctx)
	if err != nil {
		t.Fatalf("read control: %v", err)
	}
	if kind != websocket.MessageText {
		t.Fatalf("expected a control message, got %v %q", kind, payload)
	}
	var message controlMessage
	if err := json.Unmarshal(payload, &message); err != nil {
		t.Fatalf("decode control: %v", err)
	}
	return message
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

// The page is served from the binary, and everything that is not an API
// route is the page: a single document, so a reload of any path has to
// reach it rather than 404.
func TestThePageIsServedFromTheBinary(t *testing.T) {
	server, _ := startAPI(t)

	for _, testCase := range []struct{ path, want string }{
		{"/", "<title>Stormlight</title>"},
		{"/assets/app.js", "console.log('app')"},
		// A path the client routes itself; the document answers.
		{"/agents/agent-one", "<title>Stormlight</title>"},
	} {
		response := get(t, server, testCase.path)
		if response.StatusCode != http.StatusOK {
			t.Fatalf("%s answered %d", testCase.path, response.StatusCode)
		}
		body, err := io.ReadAll(response.Body)
		if err != nil {
			t.Fatalf("%s: %v", testCase.path, err)
		}
		if !strings.Contains(string(body), testCase.want) {
			t.Fatalf("%s served %q, want %q", testCase.path, body, testCase.want)
		}
	}
}

// The page loads without a token and the API does not. A browser cannot
// attach a token to its own script and stylesheet requests — they come
// from tags, not from code — so gating the static files would stop the
// page loading its own bundle. Nothing is given away: the files are
// inert, and every route that reads or changes anything is behind /api.
func TestThePageLoadsWithoutATokenButTheAPIDoesNot(t *testing.T) {
	server, _ := startAPI(t)

	for _, path := range []string{"/", "/assets/app.js"} {
		response, err := http.Get(server.URL + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		response.Body.Close()
		if response.StatusCode != http.StatusOK {
			t.Fatalf("%s answered %d without a token, want 200", path, response.StatusCode)
		}
	}

	response, err := http.Get(server.URL + "/api/agents")
	if err != nil {
		t.Fatalf("GET /api/agents: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("the API answered %d without a token, want 401", response.StatusCode)
	}
}

// An API route must never be shadowed by the catch-all that serves the
// page. ServeMux's specificity rule makes that true today; the test is
// here so a future pattern that broke it would say so, rather than the
// API quietly starting to answer in HTML.
func TestAPIRoutesOutrankThePage(t *testing.T) {
	server, _ := startAPI(t)

	response := get(t, server, "/api/agents")
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "<title>") {
		t.Fatalf("/api/agents served the page:\n%s", body)
	}
}

// A missing file is a 404, not the document. Answering a stale asset
// reference with index.html turns a plain "that file is gone" into a MIME
// error in the console, which says nothing about what happened; a path
// the client routes itself still has to reach the document.
func TestMissingFilesAreNotAnsweredWithThePage(t *testing.T) {
	server, _ := startAPI(t)

	missing := get(t, server, "/assets/index-FROM-A-PREVIOUS-BUILD.js")
	if missing.StatusCode != http.StatusNotFound {
		t.Fatalf("a missing asset answered %d, want 404", missing.StatusCode)
	}

	route := get(t, server, "/agents/agent-one")
	if route.StatusCode != http.StatusOK {
		t.Fatalf("a client route answered %d, want the document", route.StatusCode)
	}
}

// The document must never be cached: a rebuilt binary carries a new
// bundle, and a cached page would go on asking for the old one. The
// content-hashed assets beside it are the opposite case.
func TestTheDocumentIsNotCachedAndAssetsAre(t *testing.T) {
	server, _ := startAPI(t)

	page := get(t, server, "/")
	if cache := page.Header.Get("Cache-Control"); cache != "no-store" {
		t.Fatalf("the document is cached as %q", cache)
	}
	asset := get(t, server, "/assets/app.js")
	if cache := asset.Header.Get("Cache-Control"); !strings.Contains(cache, "immutable") {
		t.Fatalf("a content-hashed asset is cached as %q", cache)
	}
	if sniff := asset.Header.Get("X-Content-Type-Options"); sniff != "nosniff" {
		t.Fatalf("assets are served sniffable: %q", sniff)
	}
}

// A directory is neither a page nor a file. Left to ServeFileFS it
// answers with a redirect, and the redirect would carry the cache header
// chosen for the file being served instead — a 301 pinned for a year.
func TestADirectoryIsNotServed(t *testing.T) {
	server, _ := startAPI(t)

	// Without this the redirect is followed and never seen: what is
	// being asserted is the answer itself, not where it leads.
	client := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	response, err := client.Get(server.URL + "/assets?token=" + testToken)
	if err != nil {
		t.Fatalf("GET /assets: %v", err)
	}
	defer response.Body.Close()

	if response.StatusCode == http.StatusMovedPermanently {
		t.Fatalf("a directory answered with a redirect cached as %q",
			response.Header.Get("Cache-Control"))
	}
	if cache := response.Header.Get("Cache-Control"); strings.Contains(cache, "immutable") {
		t.Fatalf("a directory was cached as %q", cache)
	}
}

func TestAgentTranscript(t *testing.T) {
	transcriptPath := filepath.Join(t.TempDir(), "session.jsonl")
	transcript := `{"type":"user","message":{"content":"first ask"}}
{"type":"assistant","message":{"content":[{"type":"text","text":"first answer"}]}}
{"type":"user","message":{"content":"second ask"}}
`
	if err := os.WriteFile(transcriptPath, []byte(transcript), 0o644); err != nil {
		t.Fatal(err)
	}
	server, runtime := startAPI(t)
	runtime.agents[0].TranscriptPath = transcriptPath

	type view struct {
		Entries []map[string]any `json:"entries"`
		Total   int              `json:"total"`
		OK      bool             `json:"ok"`
	}
	fetch := func(t *testing.T, query string) view {
		t.Helper()
		response, err := http.Get(
			server.URL + "/api/agents/" + query + "&token=" + testToken)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		if response.StatusCode != http.StatusOK {
			t.Fatalf("status %d", response.StatusCode)
		}
		var decoded view
		if err := json.NewDecoder(response.Body).Decode(&decoded); err != nil {
			t.Fatal(err)
		}
		return decoded
	}

	full := fetch(t, "agent-one/transcript?after=0")
	if !full.OK || full.Total != 3 || len(full.Entries) != 3 {
		t.Fatalf("full fetch: %+v", full)
	}
	if full.Entries[0]["kind"] != "prompt" || full.Entries[0]["text"] != "first ask" {
		t.Errorf("first entry: %+v", full.Entries[0])
	}

	// The delta: a client that has 2 entries gets only the third.
	delta := fetch(t, "agent-one/transcript?after=2")
	if !delta.OK || delta.Total != 3 || len(delta.Entries) != 1 {
		t.Fatalf("delta fetch: %+v", delta)
	}
	if delta.Entries[0]["text"] != "second ask" {
		t.Errorf("delta entry: %+v", delta.Entries[0])
	}

	// A cursor past the end is an empty delta, not an error: the client
	// simply has everything.
	beyond := fetch(t, "agent-one/transcript?after=99")
	if !beyond.OK || beyond.Total != 3 || len(beyond.Entries) != 0 {
		t.Fatalf("beyond fetch: %+v", beyond)
	}

	// No transcript to report: ok is false, so the client keeps what it
	// has instead of blanking a morning's reading over a hiccup.
	absent := fetch(t, "missing/transcript?after=0")
	if absent.OK {
		t.Fatalf("missing agent should not be ok: %+v", absent)
	}
}

func TestAgentDiff(t *testing.T) {
	server, runtime := startAPI(t)
	// The fake agent's cwd is a plain temp dir — not a git repository —
	// so the honest answer is "nothing to show", not an empty diff.
	fetchDiff := func(t *testing.T, id string) (string, bool) {
		t.Helper()
		response := get(t, server, "/api/agents/"+id+"/diff")
		if response.StatusCode != http.StatusOK {
			t.Fatalf("status %d", response.StatusCode)
		}
		var view struct {
			Diff string `json:"diff"`
			OK   bool   `json:"ok"`
		}
		if err := json.NewDecoder(response.Body).Decode(&view); err != nil {
			t.Fatal(err)
		}
		return view.Diff, view.OK
	}
	if _, ok := fetchDiff(t, "agent-one"); ok {
		t.Error("a non-git cwd should answer ok=false")
	}

	// Now give the agent a real repository with an uncommitted edit.
	dir := runtime.agents[0].Cwd
	runGit := func(args ...string) {
		t.Helper()
		command := exec.Command("git", append([]string{"-C", dir}, args...)...)
		command.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
		)
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, output)
		}
	}
	runGit("init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(dir, "work.txt"), []byte("before\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit("add", ".")
	runGit("commit", "-q", "-m", "start")
	if err := os.WriteFile(filepath.Join(dir, "work.txt"), []byte("before\nafter\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	diff, ok := fetchDiff(t, "agent-one")
	if !ok {
		t.Fatal("expected a diff")
	}
	if !strings.Contains(diff, "+after") {
		t.Errorf("diff missing the edit:\n%s", diff)
	}
}
