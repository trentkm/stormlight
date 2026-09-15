package zed

import (
	"context"
	"fmt"
	"slices"
	"testing"
	"time"
)

func TestTargetURILeavesLocalPathsAloneAndQualifiesRemoteOnes(t *testing.T) {
	local, err := targetURI("", "/workspace/src/file.go")
	if err != nil {
		t.Fatal(err)
	}
	if local != "/workspace/src/file.go" {
		t.Fatalf("local target = %q", local)
	}
	remote, err := targetURI("cloud", "/local/home/me/work place/file.go")
	if err != nil {
		t.Fatal(err)
	}
	if remote != "ssh://cloud/local/home/me/work%20place/file.go" {
		t.Fatalf("remote target = %q", remote)
	}
}

func TestOpenDiffFocusesEachRepositoryThenRunsProjectDiff(t *testing.T) {
	oldOS := operatingSystem
	oldFind := findExecutable
	oldRun := runCommand
	oldPause := pause
	t.Cleanup(func() {
		operatingSystem = oldOS
		findExecutable = oldFind
		runCommand = oldRun
		pause = oldPause
	})

	operatingSystem = "darwin"
	findExecutable = func(name string) (string, error) {
		return "/test/" + name, nil
	}
	var calls []string
	runCommand = func(_ context.Context, name string, args ...string) ([]byte, error) {
		calls = append(calls, fmt.Sprintf("%s %v", name, args))
		return nil, nil
	}
	pause = func(time.Duration) {}

	err := OpenDiff(context.Background(), "cloud", []string{
		"/remote/one/file.go",
		"/remote/two/file.go",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"/test/zed [--new ssh://cloud/remote/one/file.go]",
		"/test/osascript [-e " + openProjectDiffScript + "]",
		"/test/zed [--new ssh://cloud/remote/two/file.go]",
		"/test/osascript [-e " + openProjectDiffScript + "]",
	}
	if !slices.Equal(calls, want) {
		t.Fatalf("calls = %#v\nwant  = %#v", calls, want)
	}
}

func TestOpenDiffExplainsUnsupportedDesktop(t *testing.T) {
	oldOS := operatingSystem
	t.Cleanup(func() { operatingSystem = oldOS })
	operatingSystem = "linux"
	if err := OpenDiff(context.Background(), "", []string{"/tmp/file"}); err == nil {
		t.Fatal("a Linux dashboard claimed it could control macOS Zed")
	}
}
