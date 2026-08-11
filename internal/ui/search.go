package ui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// transcriptSearch is the vim-style `/` search over the Spanreed transcript:
// the query, the transcript lines holding a match, and the match the
// viewport is parked on. The query survives leaving the prompt so n/N keep
// working until Esc clears it.
type transcriptSearch struct {
	input   lineInput
	query   string
	matches []int
	index   int
	// anchor is the viewport offset when the prompt opened; incremental
	// matching seeks forward from here and Esc restores it.
	anchor int
}

// Match paint is written as raw SGR rather than lipgloss styles: the
// transcript already carries its own escape codes, and raw sequences apply
// regardless of the detected color profile.
const (
	searchMatchSGR   = "\x1b[7m"           // reverse video
	searchCurrentSGR = "\x1b[30;48;5;215m" // black on soft orange
	searchResetSGR   = "\x1b[m"
)

// searchMatchLines reports which transcript lines contain the query,
// case-insensitively, ignoring styling.
func searchMatchLines(content, query string) []int {
	needle := strings.ToLower(strings.TrimSpace(query))
	if needle == "" {
		return nil
	}
	var lines []int
	for index, line := range strings.Split(content, "\n") {
		if strings.Contains(strings.ToLower(ansi.Strip(line)), needle) {
			lines = append(lines, index)
		}
	}
	return lines
}

// highlightSearchMatches repaints every query occurrence; the line holding
// the current match gets the active style so n/N movement stays visible.
func highlightSearchMatches(content, query string, currentLine int) string {
	needle := strings.ToLower(strings.TrimSpace(query))
	if needle == "" {
		return content
	}
	lines := strings.Split(content, "\n")
	for index, line := range lines {
		lines[index] = highlightSearchLine(line, needle, index == currentLine)
	}
	return strings.Join(lines, "\n")
}

// highlightSearchLine restyles each occurrence of the lowered needle inside
// one styled line. Offsets are found in the stripped text and translated to
// terminal cells, so existing ANSI styling around the matches survives.
func highlightSearchLine(line, needle string, current bool) string {
	stripped := ansi.Strip(line)
	lowered := strings.ToLower(stripped)
	if !strings.Contains(lowered, needle) {
		return line
	}
	paint := searchMatchSGR
	if current {
		paint = searchCurrentSGR
	}
	var out strings.Builder
	cell := 0
	at := 0
	for {
		hit := strings.Index(lowered[at:], needle)
		if hit < 0 {
			break
		}
		start := at + hit
		end := min(start+len(needle), len(stripped))
		startCell := ansi.StringWidth(stripped[:start])
		endCell := ansi.StringWidth(stripped[:end])
		out.WriteString(ansi.Cut(line, cell, startCell))
		out.WriteString(paint + stripped[start:end] + searchResetSGR)
		cell = endCell
		at = end
	}
	out.WriteString(ansi.Cut(line, cell, ansi.StringWidth(stripped)))
	return out.String()
}

// interactionSearchable reports whether the Spanreed pane is showing a
// transcript `/` can search: an agent must be selected, and the live PTY
// view has no line-indexed transcript to search.
func (m Model) interactionSearchable() bool {
	if m.ptyEnabled {
		return false
	}
	_, ok := m.selectedAgent()
	return ok
}

// beginSearch opens the `/` prompt over the Spanreed transcript.
func (m *Model) beginSearch() {
	m.mode = modeSearch
	m.search.input = newLineInput("search transcript")
	m.search.input.Focus()
	m.search.query = ""
	m.search.matches = nil
	m.search.index = 0
	m.search.anchor = m.interaction.YOffset()
	m.status = "Search"
}

// clearSearch drops the query and repaints the transcript unhighlighted.
func (m *Model) clearSearch() {
	m.search.query = ""
	m.search.matches = nil
	m.search.index = 0
	m.interaction.SetContent(m.interactionContent)
}

// refreshSearch recomputes matches after the transcript content changed,
// keeping the match cursor in bounds.
func (m *Model) refreshSearch() {
	m.search.matches = searchMatchLines(m.interactionContent, m.search.query)
	if m.search.index >= len(m.search.matches) {
		m.search.index = 0
	}
}

// searchDecorated is the viewport content with the active query painted in.
func (m Model) searchDecorated() string {
	if m.search.query == "" || len(m.search.matches) == 0 {
		return m.interactionContent
	}
	return highlightSearchMatches(
		m.interactionContent,
		m.search.query,
		m.search.matches[m.search.index],
	)
}

// repaintSearch pushes the decorated transcript into the viewport.
func (m *Model) repaintSearch() {
	m.interaction.SetContent(m.searchDecorated())
}

// centerSearchMatch scrolls the viewport so the current match sits mid-pane.
func (m *Model) centerSearchMatch() {
	if len(m.search.matches) == 0 {
		return
	}
	line := m.search.matches[m.search.index]
	m.interaction.SetYOffset(max(0, line-m.interaction.Height()/2))
}

// seekSearchFromAnchor parks the match cursor on the first match at or below
// where the prompt opened, wrapping to the top like vim.
func (m *Model) seekSearchFromAnchor() {
	m.search.index = 0
	for index, line := range m.search.matches {
		if line >= m.search.anchor {
			m.search.index = index
			break
		}
	}
}

// jumpSearchMatch moves n/N style through the matches, wrapping at the ends.
func (m *Model) jumpSearchMatch(delta int) {
	count := len(m.search.matches)
	if count == 0 {
		m.status = "No match: " + m.search.query
		return
	}
	m.search.index = (m.search.index + delta + count) % count
	m.repaintSearch()
	m.centerSearchMatch()
	m.status = m.searchPosition()
}

func (m Model) searchPosition() string {
	return fmt.Sprintf("Match %d/%d", m.search.index+1, len(m.search.matches))
}

// updateSearch drives the prompt: matching narrows live, Enter keeps the
// query for n/N, Esc restores the pre-search view.
func (m Model) updateSearch(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc", "ctrl+c", "ctrl+[":
		m.mode = modeNormal
		m.search.input.Blur()
		m.clearSearch()
		m.interaction.SetYOffset(m.search.anchor)
		m.status = "Ready"
		return m, nil
	case "enter":
		m.mode = modeNormal
		m.search.input.Blur()
		if m.search.query == "" {
			m.clearSearch()
			m.status = "Ready"
			return m, nil
		}
		if len(m.search.matches) == 0 {
			m.status = "No match: " + m.search.query
			return m, nil
		}
		m.status = m.searchPosition()
		return m, nil
	}
	m.search.input = m.search.input.Update(msg)
	m.applySearchQuery()
	return m, nil
}

// applySearchQuery re-matches the transcript against whatever the prompt now
// holds and moves the viewport to the first match ahead of the anchor. Typing
// and pasting both land here; nothing happens if the text is unchanged.
func (m *Model) applySearchQuery() {
	query := m.search.input.Value()
	if query == m.search.query {
		return
	}
	m.search.query = query
	m.refreshSearch()
	m.seekSearchFromAnchor()
	m.repaintSearch()
	if len(m.search.matches) > 0 {
		m.centerSearchMatch()
	} else {
		m.interaction.SetYOffset(m.search.anchor)
	}
}
