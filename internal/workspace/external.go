package workspace

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

type externalResolver struct {
	name string
	path string
}

func (r externalResolver) Name() string {
	return r.name
}

func (r externalResolver) Resolve(ctx context.Context, path string) (Context, bool, error) {
	command := exec.CommandContext(ctx, r.path, "resolve", path)
	command.Dir = path
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	err := command.Run()
	if err != nil {
		if ctx.Err() != nil {
			return Context{}, false, ctx.Err()
		}
		var exitError *exec.ExitError
		if errors.As(err, &exitError) && exitError.ExitCode() == 2 {
			return Context{}, false, nil
		}
		detail := strings.TrimSpace(stderr.String())
		if detail == "" {
			detail = err.Error()
		}
		return Context{}, false, fmt.Errorf("%s", detail)
	}

	var value Context
	if err := json.Unmarshal(stdout.Bytes(), &value); err != nil {
		return Context{}, false, fmt.Errorf("decode resolver output: %w", err)
	}
	return value, true, nil
}

func loadExternalResolvers(directory string) ([]Resolver, error) {
	if directory == "" {
		return nil, nil
	}
	entries, err := os.ReadDir(directory)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", directory, err)
	}

	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Name() < entries[j].Name()
	})
	resolvers := make([]Resolver, 0, len(entries))
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		path := filepath.Join(directory, entry.Name())
		info, err := os.Stat(path)
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 {
			continue
		}
		resolvers = append(resolvers, externalResolver{
			name: entry.Name(),
			path: path,
		})
	}
	return resolvers, nil
}
