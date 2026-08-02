package workspace

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
)

type gitResolver struct{}

func (gitResolver) Name() string {
	return "git"
}

func (gitResolver) Resolve(ctx context.Context, path string) (Context, bool, error) {
	command := exec.CommandContext(ctx,
		"git", "-C", path, "rev-parse", "--path-format=absolute",
		"--show-toplevel", "--git-common-dir",
	)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		if ctx.Err() != nil {
			return Context{}, false, ctx.Err()
		}
		if strings.Contains(strings.ToLower(stderr.String()), "not a git repository") {
			return Context{}, false, nil
		}
		return Context{}, false, fmt.Errorf("inspect Git repository: %w: %s",
			err, strings.TrimSpace(stderr.String()))
	}

	lines := strings.Split(strings.TrimSpace(stdout.String()), "\n")
	if len(lines) != 2 || lines[0] == "" || lines[1] == "" {
		return Context{}, false, fmt.Errorf("git rev-parse returned %d paths", len(lines))
	}
	executionRoot, err := canonicalDirectory(lines[0])
	if err != nil {
		return Context{}, false, err
	}
	commonDirectory, err := canonicalDirectory(lines[1])
	if err != nil {
		return Context{}, false, err
	}

	root := executionRoot
	if filepath.Base(commonDirectory) == ".git" {
		root = filepath.Dir(commonDirectory)
	}
	return Context{
		ID:            KindGit + ":" + commonDirectory,
		Kind:          KindGit,
		Name:          pathName(root),
		Root:          root,
		ExecutionRoot: executionRoot,
	}, true, nil
}
