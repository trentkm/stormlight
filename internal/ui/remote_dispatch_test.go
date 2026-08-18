package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/trentkm/stormlight/internal/agent"
	"github.com/trentkm/stormlight/internal/provider"
	"github.com/trentkm/stormlight/internal/workspace"
)

// remoteDispatchFixture is a model whose text inputs are real: the dispatch
// form drives a textarea, and a zero Model has none.
func remoteDispatchFixture(t *testing.T, backend Backend) Model {
	t.Helper()
	model := NewModel(backend)
	model.ptyEnabled = false
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	model = updated.(Model)
	model.providers = []provider.Info{{ID: agent.Provider("claude"), Label: "Claude"}}
	model.providerIndex = 0
	return model
}

func localWorkspace(root string) workspace.Context {
	return workspace.Context{
		ID: "git:" + root + "/.git", Kind: "git", Name: "api",
		Root: root, ExecutionRoot: root,
	}
}

func remoteWorkspace(host, root string) workspace.Context {
	value := localWorkspace(root)
	value.Host = host
	value.ID = host + ":" + value.ID
	return value
}

// TestTheSamePathOnTwoMachinesIsTwoChoices: folding them together would
// offer one row that starts an agent on whichever host happened to be
// listed first — and both rows look identical while it happens.
func TestTheSamePathOnTwoMachinesIsTwoChoices(t *testing.T) {
	model := remoteDispatchFixture(t, &recordingBackend{})
	model.workspaceRoots = []workspace.Context{
		localWorkspace("/srv/api"),
		remoteWorkspace("devbox", "/srv/api"),
	}
	model.initialCwd = "/srv/api"
	model.prepareDirectoryChoices("", "/srv/api")

	hosts := map[string]bool{}
	for _, choice := range model.directories {
		if choice.kind == directoryPath && choice.path == "/srv/api" {
			hosts[choice.host] = true
		}
	}
	if !hosts[""] || !hosts["devbox"] {
		t.Fatalf("both machines' /srv/api should be offered: %+v", model.directories)
	}
}

// TestDispatchCarriesTheChosenMachine: a path and the machine it is on
// are one answer. Carrying half of it starts the agent on the wrong box,
// in a directory that may well exist there.
func TestDispatchCarriesTheChosenMachine(t *testing.T) {
	backend := &recordingBackend{}
	model := remoteDispatchFixture(t, backend)
	model.workspaceRoots = []workspace.Context{remoteWorkspace("devbox", "/srv/api")}
	model.initialCwd = "/srv/api"
	model.prepareDirectoryChoices("devbox", "/srv/api")
	if model.dispatchHost != "devbox" {
		t.Fatalf("the form should be aimed at devbox: %q", model.dispatchHost)
	}
	model.taskInput.SetValue("fix the flaky test")

	if _, cmd := model.submitDispatch(); cmd == nil {
		t.Fatalf("dispatch was refused: %v", model.alert.err)
	} else {
		cmd()
	}
	if backend.request.Host != "devbox" {
		t.Fatalf("dispatch host = %q, want devbox", backend.request.Host)
	}
	if backend.request.Cwd != "/srv/api" {
		t.Fatalf("dispatch cwd = %q", backend.request.Cwd)
	}
}

// TestARemotePathIsNotCheckedAgainstThisFilesystem: /srv/api need not
// exist here, and the check that it does would either refuse a perfectly
// good dispatch or — worse — pass because something else lives there.
func TestARemotePathIsNotCheckedAgainstThisFilesystem(t *testing.T) {
	backend := &recordingBackend{}
	model := remoteDispatchFixture(t, backend)
	model.dispatchHost = "devbox"
	model.cwdInput.SetValue("/nowhere/near/this/machine")
	model.taskInput.SetValue("prove it")

	_, cmd := model.submitDispatch()
	if cmd == nil {
		t.Fatalf("a remote path must not be refused for being absent here: %v", model.alert.err)
	}
	cmd()
	if backend.request.Cwd != "/nowhere/near/this/machine" {
		t.Fatalf("cwd = %q", backend.request.Cwd)
	}

	// The same path with no host is this machine's, and is checked.
	model.dispatchHost = ""
	next, cmd := model.submitDispatch()
	if cmd != nil {
		t.Fatal("a local path that does not exist should be refused")
	}
	if next.(Model).alert.err == nil {
		t.Fatal("refusing a missing local directory should say so")
	}
}

// TestARemoteRowSaysWhichMachine: two rows reading "api  GIT  /srv/api"
// are the same row as far as anyone can tell.
func TestARemoteRowSaysWhichMachine(t *testing.T) {
	model := remoteDispatchFixture(t, &recordingBackend{})
	remote := model.renderDirectoryRow(
		directoryChoice{kind: directoryPath, host: "devbox",
			label: "api", path: "/srv/api", workspaceKind: "git"},
		false, 80,
	)
	if !strings.Contains(remote, "devbox:") {
		t.Fatalf("a remote row should name its machine: %q", remote)
	}
	local := model.renderDirectoryRow(
		directoryChoice{kind: directoryPath,
			label: "api", path: "/srv/api", workspaceKind: "git"},
		false, 80,
	)
	if strings.Contains(local, "devbox") {
		t.Fatalf("a local row should say nothing about hosts: %q", local)
	}

	// A typed path inherits the machine the form is aimed at, so the row
	// says so rather than leaving it to be discovered afterwards.
	model.dispatchHost = "devbox"
	custom := model.renderDirectoryRow(
		directoryChoice{kind: directoryCustom, label: "Enter a path"}, false, 80)
	if !strings.Contains(custom, "devbox") {
		t.Fatalf("the typed-path row should name the machine it would use: %q", custom)
	}
}

// TestPerDirectoryOverridesStayLocal: the [workspaces."/path"] table is
// keyed by directories in this machine's config, so it says nothing about
// the same string on another machine.
func TestPerDirectoryOverridesStayLocal(t *testing.T) {
	model := remoteDispatchFixture(t, &recordingBackend{})
	model.providers = []provider.Info{
		{ID: agent.Provider("codex"), Label: "Codex"},
		{ID: agent.Provider("claude"), Label: "Claude"},
	}
	model.providerForDir = func(string) (agent.Provider, bool) {
		return agent.Provider("claude"), true
	}
	model.applyDispatchOverrides("", "/srv/api")
	if model.providers[model.providerIndex].ID != agent.Provider("claude") {
		t.Fatal("a local directory's override should apply")
	}

	model.providerIndex = 0
	model.applyDispatchOverrides("devbox", "/srv/api")
	if model.providers[model.providerIndex].ID != agent.Provider("codex") {
		t.Fatal("another machine's path must not take this machine's override")
	}
}
