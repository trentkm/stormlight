package ui

// Mouse handling: wheel scroll and drag-to-copy selection.
// Split from model.go; see #34.

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

// spanreedContentTop is the screen row where the transcript viewport
// starts: header, pane title, then the agent heading's three rows.
const spanreedContentTop = 5

// handleMouse drives the transcript's two mouse behaviors: the wheel
// scrolls it (positionally, without moving keyboard focus), and
// press-drag-release highlights lines and copies them. Both stay live
// while composing or searching — reading back is most needed mid-reply.
func (m Model) handleMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	scrollable := m.mode == modeNormal ||
		m.mode == modeCompose ||
		m.mode == modeSearch
	if !m.ready || !scrollable {
		return m, nil
	}
	if msg.Button == tea.MouseButtonLeft && m.mode == modeNormal {
		return m.handleSelectionMouse(msg)
	}
	if msg.Action != tea.MouseActionPress {
		return m, nil
	}
	direction := 0
	switch msg.Button {
	case tea.MouseButtonWheelUp:
		direction = -1
	case tea.MouseButtonWheelDown:
		direction = 1
	default:
		return m, nil
	}
	// Only the transcript wheels: scrolling a list would move its
	// selection, and losing the selected agent to a stray wheel tick is
	// worse than no scroll at all.
	if m.paneAt(msg.X) != paneInteraction {
		return m, nil
	}
	m.moveSelectionIn(paneInteraction, direction*3)
	return m, nil
}

// handleSelectionMouse turns press-drag-release over the transcript into a
// line-wise selection: the drag highlights, the release copies the lines
// to the tmux buffer and system clipboard.
func (m Model) handleSelectionMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	switch msg.Action {
	case tea.MouseActionPress:
		if m.paneAt(msg.X) != paneInteraction {
			m.selectionActive = false
			return m, nil
		}
		line, ok := m.transcriptLineAt(msg.Y)
		if !ok {
			m.selectionActive = false
			return m, nil
		}
		m.selectionActive = true
		m.selectionDragging = true
		m.selectionAnchor = line
		m.selectionHead = line
		return m, nil
	case tea.MouseActionMotion:
		if !m.selectionDragging {
			return m, nil
		}
		if line, ok := m.transcriptLineAt(msg.Y); ok {
			m.selectionHead = line
		}
		return m, nil
	case tea.MouseActionRelease:
		if !m.selectionDragging {
			return m, nil
		}
		m.selectionDragging = false
		command := m.copySelectionCmd()
		// The copy is the point; once it's made the highlight has done
		// its job, like tmux's own mouse selection.
		m.selectionActive = false
		return m, command
	}
	return m, nil
}

// transcriptLineAt maps a screen row to a content line of the transcript,
// clamped to the transcript's bounds.
func (m Model) transcriptLineAt(y int) (int, bool) {
	if _, ok := m.selectedAgent(); !ok || m.interactionContent == "" {
		return 0, false
	}
	row := y - spanreedContentTop
	if row < 0 {
		row = 0
	}
	if row >= m.interaction.Height {
		row = max(0, m.interaction.Height-1)
	}
	total := m.interaction.TotalLineCount()
	if total == 0 {
		return 0, false
	}
	return clamp(m.interaction.YOffset+row, 0, total-1), true
}

// paintTranscriptSelection reverses the video of the visible rows inside
// the selected line range.
func paintTranscriptSelection(view string, offset, start, end int) string {
	lines := strings.Split(view, "\n")
	for index := range lines {
		global := offset + index
		if global < start || global > end {
			continue
		}
		lines[index] = searchMatchSGR + ansi.Strip(lines[index]) + searchResetSGR
	}
	return strings.Join(lines, "\n")
}

func (m Model) selectionRange() (int, int) {
	if m.selectionAnchor <= m.selectionHead {
		return m.selectionAnchor, m.selectionHead
	}
	return m.selectionHead, m.selectionAnchor
}

// copySelectionCmd extracts the highlighted lines and hands them to the
// clipboard: the tmux buffer plus the system clipboard through tmux's
// OSC 52 (-w) when inside tmux, pbcopy/xclip otherwise.
func (m Model) copySelectionCmd() tea.Cmd {
	start, end := m.selectionRange()
	lines := strings.Split(ansi.Strip(m.interactionContent), "\n")
	if start >= len(lines) {
		return nil
	}
	end = min(end, len(lines)-1)
	selected := make([]string, 0, end-start+1)
	for _, line := range lines[start : end+1] {
		selected = append(selected, strings.TrimRight(line, " "))
	}
	text := strings.Join(selected, "\n")
	count := end - start + 1
	return func() tea.Msg {
		if err := copyToClipboard(text); err != nil {
			return actionMsg{status: "Action failed", err: err}
		}
		label := "line"
		if count != 1 {
			label = "lines"
		}
		return actionMsg{status: fmt.Sprintf("Copied %d %s", count, label)}
	}
}

// copyToClipboard prefers tmux: load-buffer -w fills the tmux paste buffer
// and forwards to the system clipboard via OSC 52 in one step.
func copyToClipboard(text string) error {
	if os.Getenv("TMUX") != "" {
		command := exec.Command("tmux", "load-buffer", "-w", "-")
		command.Stdin = strings.NewReader(text)
		if err := command.Run(); err == nil {
			return nil
		}
	}
	for _, candidate := range [][]string{
		{"pbcopy"}, {"wl-copy"}, {"xclip", "-selection", "clipboard"},
	} {
		if _, err := exec.LookPath(candidate[0]); err != nil {
			continue
		}
		command := exec.Command(candidate[0], candidate[1:]...)
		command.Stdin = strings.NewReader(text)
		return command.Run()
	}
	return fmt.Errorf("no clipboard tool found (tmux, pbcopy, wl-copy, xclip)")
}

// paneAt maps a screen column to the dashboard pane rendered there.
func (m Model) paneAt(x int) pane {
	width := max(1, m.width-1)
	if width < 72 {
		return m.activePane
	}
	workspaceWidth, agentWidth, _ := m.paneWidths(width)
	switch {
	case x < workspaceWidth:
		return paneWorkspaces
	case x < workspaceWidth+agentWidth:
		return paneAgents
	default:
		return paneInteraction
	}
}
