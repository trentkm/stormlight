package windrun

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/trentkm/stormlight/internal/remote"
)

// MaxAgentFile bounds what a single read will pull across a tunnel. A
// transcript grows with the conversation and nothing prunes it, so the
// cap is what stops one very long-lived agent from turning a keystroke
// into a minute of transfer. A truncated transcript loses its tail; the
// renderer skips the partial line and shows the rest, which is a better
// answer than a dashboard that stops responding.
const MaxAgentFile = 64 << 20

// ReadAgentFile opens a file on the machine the agent is running on.
//
// Locally that is os.Open. On another host it is `cat` over the same SSH
// the daemon is reached through — deliberately not a new channel in the
// wire protocol, which is about terminals and knows nothing of files.
func (r *Runtime) ReadAgentFile(
	ctx context.Context,
	_ string,
	path string,
) (io.ReadCloser, error) {
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("no file to read")
	}
	if r.transport == nil {
		file, err := os.Open(path)
		if err != nil {
			return nil, err
		}
		return cappedFile{Reader: io.LimitReader(file, MaxAgentFile), closer: file}, nil
	}

	command := r.transport.CommandContext(ctx, nil, "_read", path)
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if message := strings.TrimSpace(stderr.String()); message != "" {
			return nil, fmt.Errorf("%s: %s", r.transport.Host().Name, message)
		}
		return nil, fmt.Errorf("%s: %w", r.transport.Host().Name, err)
	}
	return io.NopCloser(bytes.NewReader(stdout.Bytes())), nil
}

// cappedFile is a bounded reader that still closes the file underneath.
type cappedFile struct {
	io.Reader
	closer io.Closer
}

func (c cappedFile) Close() error { return c.closer.Close() }

// ReadHistory hands over this machine's conversation log, when this
// machine is not the one the dashboard is on.
//
// A local runtime returns nothing: the service already holds that log and
// counting it twice would put every local conversation in the browser
// twice.
func (r *Runtime) ReadHistory(ctx context.Context) (map[string][]byte, error) {
	if r.transport == nil {
		return nil, nil
	}
	host := r.transport.Host().Name
	command := r.transport.CommandContext(ctx, nil, "_history")
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if message := strings.TrimSpace(stderr.String()); message != "" {
			return nil, fmt.Errorf("%s", remote.Explain(host, message))
		}
		return nil, fmt.Errorf("%s: %w", host, err)
	}
	return map[string][]byte{host: stdout.Bytes()}, nil
}
