package ui

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/trentkm/stormlight/internal/agent"
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
		t.Fatalf("opening the remote picker was refused: %v", model.alert.err)
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
		t.Fatalf("a remote path was refused: %v", updated.(Model).alert.err)
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
	if next.(Model).alert.err == nil {
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

// TestTheNewAgentPickerBrowsesTheFormsMachine: the New Agent form can be
// aimed at another machine, and browsing this one instead sends you
// hunting for a path that was never here.
func TestTheNewAgentPickerBrowsesTheFormsMachine(t *testing.T) {
	model := flowModelFixture(t, &recordingBackend{})
	model.yaziPath = "/usr/local/bin/yazi"
	model.mode = modeDispatch
	model.dispatchHost = "devbox"
	model.cwdInput.SetValue("/srv/api")

	if _, cmd := model.openYazi(); cmd == nil {
		t.Fatalf("browsing was refused: %v", model.alert.err)
	}
	spec := yaziPickerSpec(model.dispatchHost, "", model.yaziPath)
	if spec.host != "devbox" {
		t.Fatalf("picker spec = %#v", spec)
	}

	// And with no host it is still this machine, starting where the form
	// is pointing.
	model.dispatchHost = ""
	if _, cmd := model.openYazi(); cmd == nil {
		t.Fatalf("local browsing was refused: %v", model.alert.err)
	}
}

// TestNoLocalYaziStillBrowsesAnotherMachineFromTheAgentForm: this
// machine's PATH says nothing about that one's.
func TestNoLocalYaziStillBrowsesAnotherMachineFromTheAgentForm(t *testing.T) {
	model := flowModelFixture(t, &recordingBackend{})
	model.yaziPath = ""
	model.mode = modeDispatch
	model.dispatchHost = "devbox"

	if _, cmd := model.openYazi(); cmd == nil {
		t.Fatalf("a remote browse should not need a local yazi: %v", model.alert.err)
	}
}

func checkedFixture(t *testing.T, status HostStatus, err error) Model {
	t.Helper()
	model := addWorkspaceFixture(t, "devbox")
	model.checkHost = func(context.Context, string) (HostStatus, error) {
		return status, err
	}
	model.switchAddWorkspaceTab(tabRemote)
	model.openMachine()
	// What the command would have delivered.
	updated, _ := model.Update(machineCheckedMsg{
		host: "devbox", status: status, err: err})
	return updated.(Model)
}

// TestReachingAMachineSaysSo: SSH takes as long as it takes, and a modal
// that shows nothing while it happens is one nobody can tell from a
// broken one.
func TestReachingAMachineSaysSo(t *testing.T) {
	model := addWorkspaceFixture(t, "devbox")
	model.checkHost = func(context.Context, string) (HostStatus, error) {
		return HostStatus{Ready: true, Yazi: true}, nil
	}
	model.switchAddWorkspaceTab(tabRemote)
	model.openMachine()

	if !model.machineState.running {
		t.Fatal("opening a machine should start reaching it")
	}
	status := model.renderMachineStatus(70)
	if !strings.Contains(status, "Reaching devbox") {
		t.Fatalf("status = %q", status)
	}
	// A frame of the spinner, whichever it is on.
	if !strings.ContainsAny(status, "∙●") {
		t.Fatalf("no spinner in %q", status)
	}
	// While it is unknown, browsing is still offered — silence is not a
	// verdict.
	if model.directories[0].kind != directoryYazi {
		t.Fatalf("choices = %#v", model.directories)
	}
}

// TestAMachineWithNoStormlightOffersToFixItself: the case that started
// this — a host that cannot be reached at all should say so and point at
// the row that fixes it, not offer a browse that will fail.
func TestAMachineWithNoStormlightOffersToFixItself(t *testing.T) {
	model := checkedFixture(t, HostStatus{Ready: false}, nil)

	status := model.renderMachineStatus(70)
	if !strings.Contains(status, "no Stormlight") {
		t.Fatalf("status = %q", status)
	}
	for _, choice := range model.directories {
		if choice.kind == directoryYazi {
			t.Fatal("browsing must not be offered on a machine that cannot do it")
		}
	}
	selected, ok := model.selectedDirectory()
	if !ok || selected.kind != directorySetup {
		t.Fatalf("it should open on the row that fixes this: %#v", selected)
	}
}

// TestAnUnreachableMachineSaysWhy: the error belongs on screen, not in a
// log — it is usually a host key or a name that does not resolve, and
// both have obvious fixes once seen.
func TestAnUnreachableMachineSaysWhy(t *testing.T) {
	model := checkedFixture(t, HostStatus{},
		fmt.Errorf("devbox: ssh: Could not resolve hostname devbox"))

	status := model.renderMachineStatus(70)
	if !strings.Contains(status, "Cannot reach devbox") ||
		!strings.Contains(status, "resolve hostname") {
		t.Fatalf("status = %q", status)
	}
	selected, ok := model.selectedDirectory()
	if !ok || selected.kind != directorySetup {
		t.Fatalf("selected = %#v", selected)
	}
}

// TestAMachineWithoutYaziKeepsTypedPaths: yazi's absence costs browsing
// and nothing else.
func TestAMachineWithoutYaziKeepsTypedPaths(t *testing.T) {
	model := checkedFixture(t, HostStatus{Ready: true, Yazi: false}, nil)

	if !strings.Contains(model.renderMachineStatus(70), "no yazi") {
		t.Fatalf("status = %q", model.renderMachineStatus(70))
	}
	var typed bool
	for _, choice := range model.directories {
		if choice.kind == directoryYazi {
			t.Fatal("a machine without yazi cannot be browsed")
		}
		if choice.kind == directoryCustom {
			typed = true
		}
	}
	if !typed {
		t.Fatal("typing a path still works")
	}
}

// TestAReadyMachineJustWorks: the common case stays quiet — a line
// naming the machine, and the rows as they were.
func TestAReadyMachineJustWorks(t *testing.T) {
	model := checkedFixture(t,
		HostStatus{Ready: true, Yazi: true, Detail: "Linux aarch64"}, nil)

	if !strings.Contains(model.renderMachineStatus(70), "Linux aarch64") {
		t.Fatalf("status = %q", model.renderMachineStatus(70))
	}
	if model.directories[0].kind != directoryYazi {
		t.Fatalf("choices = %#v", model.directories)
	}
}

// TestALateAnswerForAnotherMachineIsIgnored: someone who moved on before
// a slow machine replied should not have the modal change under them.
func TestALateAnswerForAnotherMachineIsIgnored(t *testing.T) {
	model := checkedFixture(t, HostStatus{Ready: true, Yazi: true}, nil)
	before := model.renderMachineStatus(70)

	updated, _ := model.Update(machineCheckedMsg{
		host: "somewhere-else", status: HostStatus{Ready: false}})
	if got := updated.(Model).renderMachineStatus(70); got != before {
		t.Fatalf("a late answer for another machine changed this one: %q", got)
	}
}

// TestACatalogedParentAndItsNestedRepoAreTellable: a catalogued parent
// directory and a Git repository inside it both end in the same
// basename, so the pane showed two rows spelled identically with the
// agents apparently on the wrong one. They are different workspaces —
// the identities are right — so they have to be readable as different.
func TestACatalogedParentAndItsNestedRepoAreTellable(t *testing.T) {
	parent := workspace.Context{
		Host: "cld2", ID: "cld2:directory:/home/t/workplace/MetaRepo-PFS",
		Kind: "directory", Name: "MetaRepo-PFS",
		Root:          "/home/t/workplace/MetaRepo-PFS",
		ExecutionRoot: "/home/t/workplace/MetaRepo-PFS",
	}
	nested := workspace.Context{
		Host: "cld2", ID: "cld2:git:/home/t/workplace/MetaRepo-PFS/src/MetaRepo-PFS/.git",
		Kind: "git", Name: "MetaRepo-PFS",
		Root:          "/home/t/workplace/MetaRepo-PFS/src/MetaRepo-PFS",
		ExecutionRoot: "/home/t/workplace/MetaRepo-PFS/src/MetaRepo-PFS",
	}

	groups := buildWorkspaceGroups(
		[]workspace.Context{parent},
		[]agent.Agent{{ID: "aaa", Host: "cld2", Workspace: nested}},
	)
	if len(groups) != 2 {
		t.Fatalf("both workspaces belong on screen: %+v", groups)
	}
	if groups[0].label == groups[1].label {
		t.Fatalf("two rows spelled the same way: %q", groups[0].label)
	}
	if groups[0].label != "workplace/MetaRepo-PFS" ||
		groups[1].label != "src/MetaRepo-PFS" {
		t.Fatalf("labels = %q, %q", groups[0].label, groups[1].label)
	}
	// The agent stays where it was; nothing was merged or moved.
	if len(groups[1].agents) != 1 || groups[1].context.ID != nested.ID {
		t.Fatalf("the agent left its own workspace: %+v", groups[1])
	}
	if len(groups[0].agents) != 0 {
		t.Fatalf("the catalogued parent gained an agent it never had: %+v", groups[0])
	}

	// And the distinguishing part survives the truncation a narrow pane
	// does, because it arrives first.
	model := Model{}
	row := model.renderWorkspaceRow(groups[1], false, false, 28, false)
	if !strings.Contains(row, "src/") {
		t.Fatalf("row = %q", row)
	}
}

// TestNamesThatDoNotCollideAreLeftAlone: growing every name would cost
// the pane its readability for a problem most workspaces do not have.
func TestNamesThatDoNotCollideAreLeftAlone(t *testing.T) {
	groups := buildWorkspaceGroups([]workspace.Context{
		{ID: "git:/a/api/.git", Kind: "git", Name: "api", Root: "/a/api"},
		{ID: "git:/b/web/.git", Kind: "git", Name: "web", Root: "/b/web"},
	}, nil)
	if groups[0].label != "api" || groups[1].label != "web" {
		t.Fatalf("labels = %q, %q", groups[0].label, groups[1].label)
	}
}

// TestTwoMachinesSameNameStillCollide: the same repository checked out on
// two machines is two rows, and the host marker alone does not spell them
// apart when both names are equal.
func TestTwoMachinesSameNameStillCollide(t *testing.T) {
	groups := buildWorkspaceGroups([]workspace.Context{
		{ID: "git:/srv/api/.git", Kind: "git", Name: "api", Root: "/srv/api"},
		{Host: "devbox", ID: "devbox:git:/opt/api/.git", Kind: "git",
			Name: "api", Root: "/opt/api"},
	}, nil)
	if groups[0].label == groups[1].label {
		t.Fatalf("both spelled %q", groups[0].label)
	}
	if groups[0].label != "srv/api" || groups[1].label != "opt/api" {
		t.Fatalf("labels = %q, %q", groups[0].label, groups[1].label)
	}
}
