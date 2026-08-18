package main

// The grid viability measurement for #103: N terminals streaming flat out
// through the real stack — daemon, attachment, widget, shared frame gate —
// rendered as a grid, with the render loop instrumented. The question it
// answers is whether a render pass stays inside the 33ms frame period as
// panes multiply; the report at exit is the data for that decision.

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/spf13/cobra"
	"github.com/trentkm/stormlight/internal/pty"
	"github.com/trentkm/stormlight/internal/selfpath"
	"github.com/trentkm/stormlight/internal/windrun"
	"github.com/trentkm/windrunner/client"
	"github.com/trentkm/windrunner/wire"
)

// benchStress floods the PTY: numbered, colored lines with no throttle, so
// the emulator and render path see the worst case the transport allows.
const benchStress = `i=0; while :; do i=$((i+1)); ` +
	`printf '\033[3%dm%07d the quick brown fox jumps over the lazy dog\033[0m\n' ` +
	`$((i % 7 + 1)) $i; done`

func newBenchCommand() *cobra.Command {
	var panes int
	var duration time.Duration
	command := &cobra.Command{
		Use:    "_bench",
		Hidden: true,
		Short:  "Measure the render loop with N streaming terminals",
		RunE: func(cmd *cobra.Command, args []string) error {
			if panes < 1 {
				return fmt.Errorf("at least one pane")
			}
			// A scratch daemon keeps stress sessions out of the real
			// roster; the child inherits WINDRUNNER_DIR and lives and
			// dies with this run's temp dir.
			scratch, err := os.MkdirTemp("", "stormlight-bench-*")
			if err != nil {
				return err
			}
			defer os.RemoveAll(scratch)
			os.Setenv("WINDRUNNER_DIR", scratch)

			binPath, err := selfpath.Resolve()
			if err != nil {
				return err
			}
			// The daemon is a child, not an EnsureDaemon orphan: a bench
			// daemon has no life after its sessions, and the wire protocol
			// has no shutdown op to stop one any other way.
			daemon := exec.Command(binPath, "_wrdaemon")
			daemon.Env = os.Environ()
			if err := daemon.Start(); err != nil {
				return err
			}
			defer func() {
				_ = daemon.Process.Kill()
				_, _ = daemon.Process.Wait()
			}()
			c, err := dialUntil(windrun.SocketPath(), 5*time.Second)
			if err != nil {
				return err
			}
			defer c.Close()

			gate := pty.NewGate()
			widgets := make([]pty.Model, 0, panes)
			sessions := make([]string, 0, panes)
			defer func() {
				for _, widget := range widgets {
					widget.Close()
				}
				for _, id := range sessions {
					_ = c.Remove(id)
				}
			}()
			for index := range panes {
				info, err := c.Spawn(wire.Request{
					Command: "/bin/sh",
					Args:    []string{"-c", benchStress},
					Cols:    80,
					Rows:    24,
					Metadata: map[string]string{
						"stormlight_bench": fmt.Sprintf("pane-%d", index),
					},
				})
				if err != nil {
					return fmt.Errorf("spawn stress pane %d: %w", index, err)
				}
				sessions = append(sessions, info.ID)
				attachment, err := c.Attach(info.ID, 256)
				if err != nil {
					return fmt.Errorf("attach stress pane %d: %w", index, err)
				}
				widget := pty.New(newBenchStream(attachment), gate, 80, 24)
				widget.SetVisible(true)
				widgets = append(widgets, widget)
			}

			model := &benchModel{
				widgets:  widgets,
				gate:     gate,
				deadline: duration,
			}
			if _, err := tea.NewProgram(model).Run(); err != nil {
				return err
			}
			fmt.Print(model.report())
			return nil
		},
	}
	command.Flags().IntVar(&panes, "panes", 4, "streaming terminals in the grid")
	command.Flags().DurationVar(&duration, "duration", 10*time.Second,
		"how long to stream before reporting")
	return command
}

// dialUntil retries the daemon socket until the child answers.
func dialUntil(socketPath string, timeout time.Duration) (*client.Client, error) {
	deadline := time.Now().Add(timeout)
	for {
		c, err := client.Dial(socketPath)
		if err == nil {
			return c, nil
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("bench daemon did not answer: %w", err)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// benchStream adapts a windrunner attachment to the widget's transport.
// The benchmark deliberately does not resync: what it measures is how the
// widget copes with more output than it can paint, and handing it state
// instead of that output would measure something else.
type benchStream struct {
	attachment *client.Attachment
	output     chan pty.Message
}

func newBenchStream(attachment *client.Attachment) benchStream {
	stream := benchStream{attachment: attachment, output: make(chan pty.Message, 256)}
	go func() {
		defer close(stream.output)
		for message := range attachment.Output() {
			// Output only: the benchmark measures how the widget copes
			// with more bytes than it can paint. Forwarding a resize or a
			// resync as an empty message would make the widget repaint
			// nothing, which is neither the real behavior nor the thing
			// being measured.
			if message.Bytes == nil {
				continue
			}
			stream.output <- pty.Message{Bytes: message.Bytes}
		}
	}()
	return stream
}

func (b benchStream) Seed() []byte               { return b.attachment.Snapshot().ANSI }
func (b benchStream) Output() <-chan pty.Message { return b.output }
func (b benchStream) Write(p []byte) error       { return b.attachment.Write(p) }
func (b benchStream) Resize(_ context.Context, cols, rows int) error {
	return b.attachment.Resize(cols, rows)
}
func (b benchStream) Close() { b.attachment.Close() }

type benchDoneMsg struct{}

type benchModel struct {
	widgets  []pty.Model
	gate     *pty.Gate
	deadline time.Duration

	width, height int
	started       time.Time
	frames        int
	renders       int
	renderTimes   []time.Duration
	elapsed       time.Duration
}

func (m *benchModel) Init() tea.Cmd {
	m.started = time.Now()
	return tea.Batch(
		m.gate.Wait(),
		tea.Tick(m.deadline, func(time.Time) tea.Msg { return benchDoneMsg{} }),
	)
}

func (m *benchModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		cols, rows := m.cellSize()
		var commands []tea.Cmd
		for _, widget := range m.widgets {
			_, cmd := widget.SetSize(cols, rows)
			if cmd != nil {
				commands = append(commands, cmd)
			}
		}
		return m, tea.Batch(commands...)
	case pty.FrameMsg:
		m.frames++
		return m, m.gate.Wait()
	case benchDoneMsg:
		m.elapsed = time.Since(m.started)
		return m, tea.Quit
	case tea.KeyPressMsg:
		if msg.String() == "q" || msg.String() == "ctrl+c" {
			m.elapsed = time.Since(m.started)
			return m, tea.Quit
		}
	}
	return m, nil
}

// cellSize divides the window into a two-column grid, leaving one status
// row at the bottom.
func (m *benchModel) cellSize() (int, int) {
	columns := min(2, len(m.widgets))
	rows := (len(m.widgets) + columns - 1) / columns
	width := max(2, m.width/max(1, columns))
	height := max(2, (m.height-1)/max(1, rows))
	return width, height
}

func (m *benchModel) View() tea.View {
	start := time.Now()
	columns := min(2, len(m.widgets))
	var bands []string
	for base := 0; base < len(m.widgets); base += columns {
		row := make([]string, 0, columns)
		for _, widget := range m.widgets[base:min(base+columns, len(m.widgets))] {
			row = append(row, widget.View())
		}
		bands = append(bands, lipgloss.JoinHorizontal(lipgloss.Top, row...))
	}
	body := strings.Join(bands, "\n")
	cost := time.Since(start)
	m.renders++
	m.renderTimes = append(m.renderTimes, cost)
	status := fmt.Sprintf(
		" %d panes streaming · %d frames · render %s · q quits",
		len(m.widgets), m.frames, cost.Round(time.Microsecond),
	)
	return tea.NewView(body + "\n" + status)
}

// report summarizes the run: the pass criterion is the render pass staying
// inside the 33ms frame period with every pane busy.
func (m *benchModel) report() string {
	if m.elapsed <= 0 || len(m.renderTimes) == 0 {
		return "bench: no frames rendered\n"
	}
	times := slices.Clone(m.renderTimes)
	slices.Sort(times)
	var total time.Duration
	for _, t := range times {
		total += t
	}
	mean := total / time.Duration(len(times))
	p95 := times[len(times)*95/100]
	worst := times[len(times)-1]
	over := 0
	for _, t := range times {
		if t > 33*time.Millisecond {
			over++
		}
	}
	return fmt.Sprintf(
		"panes\t%d\nduration\t%s\nframes\t%d (%.1f/s)\nrenders\t%d\n"+
			"render mean\t%s\nrender p95\t%s\nrender max\t%s\nover 33ms\t%d\n",
		len(m.widgets),
		m.elapsed.Round(time.Millisecond),
		m.frames,
		float64(m.frames)/m.elapsed.Seconds(),
		m.renders,
		mean.Round(time.Microsecond),
		p95.Round(time.Microsecond),
		worst.Round(time.Microsecond),
		over,
	)
}
