package workspace

import "testing"

func TestContextTail(t *testing.T) {
	for _, testCase := range []struct {
		name  string
		value Context
		want  string
	}{
		{
			name: "component wins",
			value: Context{
				Name:          "monorepo",
				Root:          "/repos/monorepo",
				ExecutionRoot: "/repos/monorepo/services/api",
				ComponentName: "api",
			},
			want: "api",
		},
		{
			name: "worktree stands in for a missing component",
			value: Context{
				Name:          "stormlight",
				Root:          "/repos/stormlight",
				ExecutionRoot: "/repos/stormlight/.claude/worktrees/status-bar",
			},
			want: "status-bar",
		},
		{
			name: "main checkout has no tail",
			value: Context{
				Name:          "stormlight",
				Root:          "/repos/stormlight",
				ExecutionRoot: "/repos/stormlight",
			},
			want: "",
		},
		{
			name: "a tail that repeats the name says nothing",
			value: Context{
				Name:          "api",
				Root:          "/repos/monorepo",
				ExecutionRoot: "/repos/monorepo/api",
				ComponentName: "api",
			},
			want: "",
		},
	} {
		if got := testCase.value.Tail(); got != testCase.want {
			t.Fatalf("%s: tail = %q, want %q", testCase.name, got, testCase.want)
		}
	}
}

// TestOnHostQualifiesIdentity: two machines can hold the same path, and
// an unqualified ID would merge them into one workspace group holding
// agents that cannot see each other's files.
func TestOnHostQualifiesIdentity(t *testing.T) {
	local := Context{
		ID:            "git:/srv/api/.git",
		Kind:          KindGit,
		Name:          "api",
		Root:          "/srv/api",
		ExecutionRoot: "/srv/api",
	}
	remote := local.OnHost("devbox")

	if remote.Host != "devbox" {
		t.Fatalf("host not recorded: %+v", remote)
	}
	if remote.ID == local.ID {
		t.Fatalf("the same id on two machines is two workspaces: %q", remote.ID)
	}
	if remote.ID != "devbox:git:/srv/api/.git" {
		t.Fatalf("id = %q", remote.ID)
	}
	// Paths belong to the far side and are not rewritten.
	if remote.Root != local.Root || remote.ExecutionRoot != local.ExecutionRoot {
		t.Fatalf("paths must stay the host's: %+v", remote)
	}
	// Stamping is idempotent: a context that already names its host is
	// not qualified twice as it passes through the registry again.
	if again := remote.OnHost("devbox"); again.ID != remote.ID {
		t.Fatalf("qualification is not idempotent: %q", again.ID)
	}
	// And this machine's workspaces are untouched by any of it.
	if local.OnHost("").ID != local.ID {
		t.Fatalf("a local context must not be qualified")
	}
}
