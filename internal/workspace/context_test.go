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
