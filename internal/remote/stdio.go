package remote

import (
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// stdioConn is a net.Conn over a subprocess's stdin and stdout — the
// shape an `ssh` invocation has when the far end is a program that
// speaks a protocol rather than a shell.
//
// The pipes are made with os.Pipe rather than exec's StdinPipe/StdoutPipe
// so that both ends are *os.File and deadlines are real. windrunner
// bounds every control call with SetDeadline, and a transport that
// ignored it would turn an unanswerable call into a permanent hang.
type stdioConn struct {
	cmd    *exec.Cmd
	r      *os.File
	w      *os.File
	stderr *syncBuffer

	done      chan struct{}
	waitErr   error
	closeOnce sync.Once
	address   sshAddr
}

type sshAddr string

func (a sshAddr) Network() string { return "ssh" }
func (a sshAddr) String() string  { return string(a) }

// syncBuffer collects the child's stderr without racing the reader that
// reports it.
type syncBuffer struct {
	mu      sync.Mutex
	builder strings.Builder
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.builder.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return strings.TrimSpace(b.builder.String())
}

// dialCommand starts a command and returns its stdio as a connection.
func dialCommand(cmd *exec.Cmd, address string) (*stdioConn, error) {
	childIn, parentWrite, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	parentRead, childOut, err := os.Pipe()
	if err != nil {
		childIn.Close()
		parentWrite.Close()
		return nil, err
	}
	stderr := &syncBuffer{}
	cmd.Stdin = childIn
	cmd.Stdout = childOut
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		childIn.Close()
		childOut.Close()
		parentRead.Close()
		parentWrite.Close()
		return nil, err
	}
	// The child holds its own ends now; a copy left open here would keep
	// the pipes from ever reporting EOF.
	childIn.Close()
	childOut.Close()

	conn := &stdioConn{
		cmd:     cmd,
		r:       parentRead,
		w:       parentWrite,
		stderr:  stderr,
		done:    make(chan struct{}),
		address: sshAddr(address),
	}
	go func() {
		conn.waitErr = cmd.Wait()
		close(conn.done)
	}()
	return conn, nil
}

func (c *stdioConn) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	if err != nil && errors.Is(err, io.EOF) {
		// The stream ended because the process did. Its own complaint is
		// far more useful than "EOF" — this is where "Permission denied
		// (publickey)" and "command not found" come from.
		if failure := c.exitFailure(); failure != nil {
			return n, failure
		}
	}
	return n, err
}

func (c *stdioConn) Write(p []byte) (int, error) { return c.w.Write(p) }

// exitFailure reports the child's own error once it has exited, or nil if
// it exited cleanly. It waits only briefly: stdout is already at EOF, so
// the process is on its way out, and a hang here would be worse than a
// vague error.
func (c *stdioConn) exitFailure() error {
	select {
	case <-c.done:
	case <-time.After(3 * time.Second):
		return nil
	}
	if c.waitErr == nil {
		return nil
	}
	if message := c.stderr.String(); message != "" {
		return errors.New(Explain(string(c.address), message))
	}
	return fmt.Errorf("%s: %w", c.address, c.waitErr)
}

// Close ends the connection and lets the child go. Closing our end of its
// stdin is the polite exit — ssh reads EOF and leaves — and the kill is
// the backstop for one that does not, scheduled rather than waited on so
// that detaching a terminal never blocks the dashboard.
func (c *stdioConn) Close() error {
	c.closeOnce.Do(func() {
		c.w.Close()
		c.r.Close()
		go func() {
			select {
			case <-c.done:
			case <-time.After(2 * time.Second):
				if c.cmd.Process != nil {
					_ = c.cmd.Process.Kill()
				}
			}
		}()
	})
	return nil
}

func (c *stdioConn) LocalAddr() net.Addr  { return sshAddr("local") }
func (c *stdioConn) RemoteAddr() net.Addr { return c.address }

func (c *stdioConn) SetDeadline(t time.Time) error {
	return errors.Join(c.r.SetReadDeadline(t), c.w.SetWriteDeadline(t))
}

func (c *stdioConn) SetReadDeadline(t time.Time) error  { return c.r.SetReadDeadline(t) }
func (c *stdioConn) SetWriteDeadline(t time.Time) error { return c.w.SetWriteDeadline(t) }
