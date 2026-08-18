package ui

import (
	"strings"
	"testing"

	"github.com/trentkm/stormlight/internal/workspace"
)

func addWorkspaceFixture(t *testing.T, hosts ...string) Model {
	t.Helper()
	model := flowModelFixture(t, &recordingBackend{})
	choices := make([]HostChoice, 0, len(hosts))
	for _, host := range hosts {
		choices = append(choices, HostChoice{Name: host, Summary: "trent@" + host})
	}
	model.machines = machineChoices(choices)
	model.yaziPath = "/usr/local/bin/yazi"
	model.mode = modeAddWorkspace
	model.prepareAddWorkspaceChoices(t.TempDir())
	return model
}

// TestTheModalOpensOnLocal: adding a workspace here is the common case
// and must stay the thing that happens if you press nothing.
func TestTheModalOpensOnLocal(t *testing.T) {
	model := addWorkspaceFixture(t, "sandbox", "devbox")
	if model.addWorkspaceTab != tabLocal || model.addWorkspaceHostName() != "" {
		t.Fatalf("opened on tab %v, host %q", model.addWorkspaceTab, model.addWorkspaceHostName())
	}
	if model.showingMachines() {
		t.Fatal("the Local tab asks for a directory, not a machine")
	}
}

// TestTheRemoteTabListsTheSSHConfigHosts, and the row for a machine it
// does not name — most people's configuration names nothing at all.
func TestTheRemoteTabListsTheSSHConfigHosts(t *testing.T) {
	model := addWorkspaceFixture(t, "sandbox", "devbox")
	model.switchAddWorkspaceTab(tabRemote)

	if !model.showingMachines() {
		t.Fatal("the Remote tab asks which machine first")
	}
	if len(model.machines) != 3 {
		t.Fatalf("machines = %#v", model.machines)
	}
	if model.machines[0].name != "sandbox" || model.machines[2].kind != machineTyped {
		t.Fatalf("machines = %#v", model.machines)
	}
	rendered := strings.Join(model.renderMachineRows(70, 5), "\n")
	// The summary is what tells one `builder` from another.
	if !strings.Contains(rendered, "trent@sandbox") {
		t.Fatalf("rows = %q", rendered)
	}
}

// TestOpeningAMachineDrillsIn: choosing one moves to its directories,
// with the machine as a breadcrumb rather than a third tab.
func TestOpeningAMachineDrillsIn(t *testing.T) {
	model := addWorkspaceFixture(t, "sandbox", "devbox")
	model.switchAddWorkspaceTab(tabRemote)
	model.selectMachine(1)
	model.openMachine()

	if model.addWorkspaceHostName() != "devbox" {
		t.Fatalf("host = %q", model.addWorkspaceHostName())
	}
	if model.showingMachines() {
		t.Fatal("it should be asking for a directory now")
	}
	if !strings.Contains(model.renderAddWorkspaceTabs(70), "devbox") {
		t.Fatalf("tabs = %q", model.renderAddWorkspaceTabs(70))
	}
	// The rows say which machine they act on.
	choice, ok := model.selectedDirectory()
	if !ok || choice.host != "devbox" || !strings.Contains(choice.detail, "devbox") {
		t.Fatalf("directory choice = %#v", choice)
	}

	// Esc steps back to the list rather than abandoning the modal.
	model.leaveMachine()
	if !model.showingMachines() || model.addWorkspaceHostName() != "" {
		t.Fatal("leaving a machine should return to the machine list")
	}
}

// TestNamingAMachineTheConfigDoesNot: a host is known by being named, so
// the modal takes a destination nobody wrote down.
func TestNamingAMachineTheConfigDoesNot(t *testing.T) {
	model := addWorkspaceFixture(t)
	model.switchAddWorkspaceTab(tabRemote)
	if len(model.machines) != 1 || model.machines[0].kind != machineTyped {
		t.Fatalf("an empty ssh config still offers the typed row: %#v", model.machines)
	}

	model.openMachine()
	if model.formFocus != dispatchCustomPath {
		t.Fatal("the typed row should ask for the destination")
	}
	model.hostInput.SetValue("trent@newbox")
	model.enterMachine(strings.TrimSpace(model.hostInput.Value()))
	if model.addWorkspaceHostName() != "trent@newbox" {
		t.Fatalf("host = %q", model.addWorkspaceHostName())
	}
}

// TestBrowsingFollowsTheChosenMachine: the whole point — the picker runs
// where the directories are.
func TestBrowsingFollowsTheChosenMachine(t *testing.T) {
	model := addWorkspaceFixture(t, "devbox")
	model.switchAddWorkspaceTab(tabRemote)
	model.openMachine()

	choice, ok := model.selectedDirectory()
	if !ok || choice.kind != directoryYazi || choice.host != "devbox" {
		t.Fatalf("directory choice = %#v", choice)
	}

	// The start directory is this machine's and means nothing there, so
	// it is not sent: that Stormlight starts wherever it lands.
	spec := yaziPickerSpec(model.addWorkspaceHostName(), "", model.yaziPath)
	if spec.host != "devbox" {
		t.Fatalf("picker spec = %#v", spec)
	}
	if strings.Contains(strings.Join(spec.args, " "), "--yazi") {
		t.Fatalf("this machine's yazi means nothing there: %#v", spec.args)
	}
}

// TestAMissingLocalYaziDoesNotHideARemoteOne: this machine's PATH says
// nothing about another machine's.
func TestAMissingLocalYaziDoesNotHideARemoteOne(t *testing.T) {
	model := addWorkspaceFixture(t, "devbox")
	model.yaziPath = ""
	model.setAddWorkspaceChoices()
	if model.directories[0].kind == directoryYazi {
		t.Fatal("no yazi here means no browse row here")
	}

	model.switchAddWorkspaceTab(tabRemote)
	model.openMachine()
	if model.directories[0].kind != directoryYazi {
		t.Fatalf("browsing another machine should still be offered: %#v", model.directories)
	}
	if _, cmd := model.openYazi(); cmd == nil {
		t.Fatalf("opening the remote picker was refused: %v", model.err)
	}
}

// TestATypedRemotePathIsNotCheckedHere: /srv/api need not exist on this
// machine, and checking would either refuse a good path or pass for the
// wrong reason.
func TestATypedRemotePathIsNotCheckedHere(t *testing.T) {
	model := addWorkspaceFixture(t, "devbox")
	model.switchAddWorkspaceTab(tabRemote)
	model.openMachine()

	updated, cmd := model.submitAddWorkspace("/srv/api")
	if cmd == nil {
		t.Fatalf("a remote path was refused: %v", updated.(Model).err)
	}
	message, ok := cmd().(workspaceAddedMsg)
	if !ok || message.err != nil {
		t.Fatalf("add result = %#v", message)
	}
	backend := model.backend.(*recordingBackend)
	if backend.addedHost != "devbox" || backend.addedPath != "/srv/api" {
		t.Fatalf("added %q on %q", backend.addedPath, backend.addedHost)
	}

	// A relative path means "here", and here is the wrong machine.
	next, cmd := model.submitAddWorkspace("srv/api")
	if cmd != nil {
		t.Fatal("a relative remote path must be refused")
	}
	if next.(Model).err == nil {
		t.Fatal("refusing it should say why")
	}
}

// TestAWorkspaceRowNamesItsMachine: two checkouts at the same path on
// different machines are otherwise the same row twice.
func TestAWorkspaceRowNamesItsMachine(t *testing.T) {
	remote := workspace.Context{
		Host: "devbox", ID: "devbox:git:/srv/api/.git", Kind: "git",
		Name: "api", Root: "/srv/api", ExecutionRoot: "/srv/api",
	}
	detail := workspaceDetail(remote, 60)
	if !strings.Contains(detail, "devbox") {
		t.Fatalf("subtitle = %q", detail)
	}

	local := remote
	local.Host = ""
	if strings.Contains(workspaceDetail(local, 60), "devbox") {
		t.Fatalf("a local workspace says nothing about hosts: %q", workspaceDetail(local, 60))
	}
}
