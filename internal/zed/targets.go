// Package zed bridges a managed agent's repository to the Zed instance on
// the dashboard machine.
package zed

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
)

// DiffTargets returns one existing path per changed Git repository under
// start. Opening a file rather than only its containing directory makes the
// repository active in Zed before project diff is invoked.
//
// A path inside a Git repository resolves directly. A Brazil workspace root
// is the other important shape: each package is its own repository under
// src/, so all changed package repositories are returned.
func DiffTargets(ctx context.Context, start string) ([]string, error) {
	directory, err := diffDirectory(start)
	if err != nil {
		return nil, err
	}

	if root, ok := repositoryRoot(ctx, directory); ok {
		target, changed, err := repositoryDiffTarget(ctx, root)
		if err != nil {
			return nil, err
		}
		if !changed {
			return nil, fmt.Errorf("no Git changes found in %s", root)
		}
		return []string{target}, nil
	}

	roots, err := brazilPackageRepositories(ctx, directory)
	if err != nil {
		return nil, err
	}
	var targets []string
	for _, root := range roots {
		target, changed, targetErr := repositoryDiffTarget(ctx, root)
		if targetErr != nil {
			return nil, targetErr
		}
		if changed {
			targets = append(targets, target)
		}
	}
	if len(targets) == 0 {
		return nil, fmt.Errorf("no Git changes found under %s", directory)
	}
	return targets, nil
}

func diffDirectory(start string) (string, error) {
	if strings.TrimSpace(start) == "" {
		var err error
		start, err = os.Getwd()
		if err != nil {
			return "", fmt.Errorf("get current directory: %w", err)
		}
	}
	absolute, err := filepath.Abs(start)
	if err != nil {
		return "", fmt.Errorf("resolve %q: %w", start, err)
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", fmt.Errorf("resolve %q: %w", absolute, err)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", fmt.Errorf("inspect %q: %w", resolved, err)
	}
	if !info.IsDir() {
		resolved = filepath.Dir(resolved)
	}
	return filepath.Clean(resolved), nil
}

func repositoryRoot(ctx context.Context, directory string) (string, bool) {
	output, err := gitOutput(ctx, directory, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", false
	}
	root := strings.TrimSpace(string(output))
	if root == "" || !filepath.IsAbs(root) {
		return "", false
	}
	return filepath.Clean(root), true
}

// brazilPackageRepositories recognizes the filesystem contract Brazil owns:
// a workspace root contains src/, and each immediate child there is a package
// repository. It deliberately does not walk the whole tree — build/, env/,
// logs/, and nested dependencies can be enormous.
func brazilPackageRepositories(
	ctx context.Context,
	directory string,
) ([]string, error) {
	source := directory
	if filepath.Base(source) != "src" {
		source = filepath.Join(source, "src")
	}
	entries, err := os.ReadDir(source)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("%s is neither a Git repository nor a Brazil workspace", directory)
		}
		return nil, fmt.Errorf("read Brazil packages under %s: %w", source, err)
	}

	seen := make(map[string]bool)
	var roots []string
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		root, ok := repositoryRoot(ctx, filepath.Join(source, entry.Name()))
		if !ok || seen[root] {
			continue
		}
		seen[root] = true
		roots = append(roots, root)
	}
	if len(roots) == 0 {
		return nil, fmt.Errorf("%s contains no Git package repositories", source)
	}
	slices.Sort(roots)
	return roots, nil
}

func repositoryDiffTarget(
	ctx context.Context,
	root string,
) (target string, changed bool, err error) {
	paths, err := repositoryChangedPaths(ctx, root)
	if err != nil {
		return "", false, fmt.Errorf("inspect Git changes in %s: %w", root, err)
	}
	if len(paths) == 0 {
		return "", false, nil
	}
	for _, path := range paths {
		candidate, ok := repositoryPath(root, path)
		if !ok {
			continue
		}
		info, statErr := os.Stat(candidate)
		if statErr == nil && !info.IsDir() {
			return candidate, true, nil
		}
	}

	// A repository whose only change is deletion still needs an existing
	// file to make it active in a workspace containing several repositories.
	tracked, _ := gitOutput(ctx, root, "ls-files", "-z")
	for _, path := range nulPaths(tracked) {
		candidate, ok := repositoryPath(root, path)
		if !ok {
			continue
		}
		info, statErr := os.Stat(candidate)
		if statErr == nil && !info.IsDir() {
			return candidate, true, nil
		}
	}
	return root, true, nil
}

func repositoryChangedPaths(ctx context.Context, root string) ([]string, error) {
	var arguments []string
	if base := repositoryMergeBase(ctx, root); base != "" {
		// A bare base commit compares committed, staged, and unstaged work
		// against the working tree. The three-dot spelling would stop at
		// HEAD and hide the uncommitted half.
		arguments = []string{"diff", "--name-only", "-z", base, "--"}
	} else if gitSucceeds(ctx, root, "rev-parse", "--verify", "--quiet", "HEAD") {
		arguments = []string{"diff", "--name-only", "-z", "HEAD", "--"}
	} else {
		arguments = []string{"diff", "--cached", "--name-only", "-z", "--"}
	}

	tracked, err := gitOutput(ctx, root, arguments...)
	if err != nil {
		return nil, err
	}
	untracked, err := gitOutput(
		ctx,
		root,
		"ls-files", "--others", "--exclude-standard", "-z",
	)
	if err != nil {
		return nil, err
	}
	paths := append(nulPaths(tracked), nulPaths(untracked)...)
	slices.Sort(paths)
	return slices.Compact(paths), nil
}

func repositoryMergeBase(ctx context.Context, root string) string {
	var refs []string
	if output, err := gitOutput(
		ctx,
		root,
		"symbolic-ref", "--quiet", "--short", "refs/remotes/origin/HEAD",
	); err == nil {
		if ref := strings.TrimSpace(string(output)); ref != "" {
			refs = append(refs, ref)
		}
	}
	refs = append(refs,
		"origin/main",
		"origin/mainline",
		"origin/master",
		"main",
		"mainline",
		"master",
	)
	refs = slices.Compact(refs)
	for _, ref := range refs {
		if !gitSucceeds(ctx, root, "rev-parse", "--verify", "--quiet", ref) {
			continue
		}
		output, err := gitOutput(ctx, root, "merge-base", ref, "HEAD")
		if err == nil {
			return strings.TrimSpace(string(output))
		}
	}
	return ""
}

func repositoryPath(root, name string) (string, bool) {
	if filepath.IsAbs(name) {
		return "", false
	}
	candidate := filepath.Clean(filepath.Join(root, name))
	relative, err := filepath.Rel(root, candidate)
	if err != nil || relative == ".." ||
		strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", false
	}
	return candidate, true
}

func nulPaths(output []byte) []string {
	raw := strings.Split(strings.TrimSuffix(string(output), "\x00"), "\x00")
	paths := raw[:0]
	for _, path := range raw {
		if path != "" {
			paths = append(paths, path)
		}
	}
	return paths
}

func gitSucceeds(ctx context.Context, directory string, args ...string) bool {
	_, err := gitOutput(ctx, directory, args...)
	return err == nil
}

func gitOutput(ctx context.Context, directory string, args ...string) ([]byte, error) {
	gitArgs := append(
		[]string{"--no-pager", "--no-optional-locks", "-C", directory},
		args...,
	)
	command := exec.CommandContext(ctx, "git", gitArgs...)
	return command.Output()
}
