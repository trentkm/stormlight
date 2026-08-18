package ui

import (
	"strings"
	"testing"

	"github.com/trentkm/stormlight/internal/workspace"
)

func addWorkspaceFixture(t *testing.T, hosts ...string) Model {
	t.Helper()
	model := flowModelFixture(t, &recordingBackend{})
	model.hosts = append([]string{""}, hosts...)
	model.yaziPath = "/usr/local/bin/yazi"
	model.mode = modeAddWorkspace
	model.formFocus = dispatchDirectory
	model.prepareAddWorkspaceChoices(t.TempDir())
	return model
}

// TestTheModalStartsOnThisMachine: adding a workspace here is the common
// case and must stay the thing that happens if you press nothing.
func TestTheModalStartsOnThisMachine(t *testing.T) {
	model := addWorkspaceFixture(t, "sandbox", "devbox")
	if model.addWorkspaceHostName() != "" {
		t.Fatalf("started on %q", model.addWorkspaceHostName())
	}
	if !strings.Contains(model.renderMachineStrip(60), "This machine") {
		t.Fatalf("strip = %q", model.renderMachineStrip(60))
	}
}

func TestTheMachineStripWalksTheSSHConfigHosts(t *testing.T) {
	model := addWorkspaceFixture(t, "sandbox", "devbox")

	model.selectAddWorkspaceHost(1)
	if model.addWorkspaceHostName() != "sandbox" {
		t.Fatalf("host = %q", model.addWorkspaceHostName())
	}
	model.selectAddWorkspaceHost(1)
	if model.addWorkspaceHostName() != "devbox" {
		t.Fatalf("host = %q", model.addWorkspaceHostName())
	}
	// It wraps: the list is short, and a dead end is a keypress that
	// silently does nothing.
	model.selectAddWorkspaceHost(1)
	if model.addWorkspaceHostName() != "" {
		t.Fatalf("host = %q, want back to this machine", model.addWorkspaceHostName())
	}
	model.selectAddWorkspaceHost(-1)
	if model.addWorkspaceHostName() != "devbox" {
		t.Fatalf("host = %q, want the last one", model.addWorkspaceHostName())
	}
}

// TestWithNoOtherMachinesTheStripSaysSo: an empty ~/.ssh/config is
// ordinary, and a row that cannot move should say why rather than ignore
// the key.
func TestWithNoOtherMachinesTheStripSaysSo(t *testing.T) {
	model := addWorkspaceFixture(t)
	strip := model.renderMachineStrip(60)
	if !strings.Contains(strip, "ssh/config") {
		t.Fatalf("strip = %q", strip)
	}
	model.selectAddWorkspaceHost(1)
	if model.addWorkspaceHostName() != "" {
		t.Fatal("there is nowhere else to go")
	}
}

// TestBrowsingFollowsTheChosenMachine: the whole point — the picker runs
// where the directories are.
func TestBrowsingFollowsTheChosenMachine(t *testing.T) {
	model := addWorkspaceFixture(t, "devbox")
	model.selectAddWorkspaceHost(1)

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
	if _, ok := model.selectedDirectory(); ok &&
		model.directories[0].kind == directoryYazi {
		t.Fatal("no yazi here means no browse row here")
	}

	model.selectAddWorkspaceHost(1)
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
	model.selectAddWorkspaceHost(1)

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
