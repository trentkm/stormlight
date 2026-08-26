package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/trentkm/stormlight/internal/agent"
)

func request(t *testing.T, server *httptest.Server, method, path, body string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(method, server.URL+path+"?token="+testToken, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	t.Cleanup(func() { response.Body.Close() })
	return response
}

func TestLinksOverHTTP(t *testing.T) {
	t.Setenv("STORMLIGHT_LINKS_FILE", filepath.Join(t.TempDir(), "links.json"))
	server, runtime := startAPI(t)
	runtime.mu.Lock()
	runtime.agents = append(runtime.agents, agent.Agent{
		ID:       "agent-two",
		Provider: agent.ProviderClaude,
		Name:     "reviewer",
		Activity: agent.ActivityIdle,
	})
	runtime.mu.Unlock()

	// Empty is [], not null.
	response := get(t, server, "/api/links")
	var listed []map[string]any
	if err := json.NewDecoder(response.Body).Decode(&listed); err != nil || len(listed) != 0 {
		t.Fatalf("empty list: %v, %v", err, listed)
	}

	// A self-link and an unknown agent are refused with the right status.
	if r := post(t, server, "/api/links", `{"from":"agent-one","to":"agent-one"}`); r.StatusCode != http.StatusConflict {
		t.Fatalf("self-link status = %d", r.StatusCode)
	}
	if r := post(t, server, "/api/links", `{"from":"agent-one","to":"nobody"}`); r.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown agent status = %d", r.StatusCode)
	}

	response = post(t, server, "/api/links", `{"from":"agent-one","to":"agent-two","label":"review","auto":true}`)
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("create status = %d", response.StatusCode)
	}
	var created map[string]any
	if err := json.NewDecoder(response.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	id := created["id"].(string)
	if created["from"] != "agent-one" || created["to"] != "agent-two" || created["auto"] != true {
		t.Fatalf("created = %v", created)
	}

	// The loop back is a conflict.
	if r := post(t, server, "/api/links", `{"from":"agent-two","to":"agent-one"}`); r.StatusCode != http.StatusConflict {
		t.Fatalf("cycle status = %d", r.StatusCode)
	}

	// Only the label and the flag change.
	response = request(t, server, http.MethodPatch, "/api/links/"+id, `{"label":"review harder","auto":false}`)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("patch status = %d", response.StatusCode)
	}
	var patched map[string]any
	json.NewDecoder(response.Body).Decode(&patched)
	if patched["label"] != "review harder" || patched["auto"] != false {
		t.Fatalf("patched = %v", patched)
	}

	// Firing with nothing said is a conflict; with a reply, it sends.
	if r := post(t, server, "/api/links/"+id+"/fire", ""); r.StatusCode != http.StatusConflict {
		t.Fatalf("fire before reply status = %d", r.StatusCode)
	}
	runtime.mu.Lock()
	runtime.agents[0].LastReply = "the tests pass"
	runtime.agents[0].Activity = agent.ActivityIdle
	runtime.mu.Unlock()
	if r := post(t, server, "/api/links/"+id+"/fire", ""); r.StatusCode != http.StatusOK {
		t.Fatalf("fire status = %d", r.StatusCode)
	}
	runtime.mu.Lock()
	sent := append([]string(nil), runtime.sent...)
	runtime.mu.Unlock()
	if len(sent) != 1 || !strings.HasPrefix(sent[0], "agent-two: review harder\n\nFrom cl-tests:\nthe tests pass") {
		t.Fatalf("sent = %q", sent)
	}

	if r := request(t, server, http.MethodDelete, "/api/links/"+id, ""); r.StatusCode != http.StatusNoContent {
		t.Fatalf("delete status = %d", r.StatusCode)
	}
	if r := request(t, server, http.MethodDelete, "/api/links/"+id, ""); r.StatusCode != http.StatusNotFound {
		t.Fatalf("delete again status = %d", r.StatusCode)
	}
}
