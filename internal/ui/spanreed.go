package ui

// The Spanreed transcript: rendering and the capture-cleaning pipeline.
// Split from model.go; see #34.

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/trentkm/stormlight/internal/agent"
)

func (m Model) renderInteraction(width, height int) string {
	managedAgent, ok := m.selectedAgent()
	if !ok {
		return mutedStyle().Render(truncate("No agent selected", width))
	}
	if m.ptyEnabled {
		return m.renderPTYInteraction(managedAgent, width, height)
	}
	title := titleStyle().Render(truncate(agentDisplayTitle(managedAgent), width))
	if rowEmphasisFor(managedAgent) == emphasisWorking {
		title = shimmerText(
			truncate(agentDisplayTitle(managedAgent), width),
			m.shimmerPhaseOrRest(),
			nil,
		)
	}
	meta := interactionMeta(managedAgent, width)
	switch {
	case managedAgent.EffectiveMark() == agent.MarkAttention:
		meta = lipgloss.NewStyle().Foreground(colorWaiting()).
			Render(truncate("Marked needs attention", width))
	case managedAgent.EffectiveMark() == agent.MarkWorking:
		// The human said it is still going, and the meta line already reads
		// "marked in progress"; nothing derived may talk over that.
	case managedAgent.ProcessLive && managedAgent.Attention.Urgent():
		// Prompts are answered in the agent's own terminal; the dashboard's
		// job is pointing at the pane, loudly.
		hint := " — i to reply"
		if managedAgent.Attention.TerminalOwned() {
			hint = " — Enter opens the terminal"
		}
		meta = lipgloss.NewStyle().Foreground(colorWaiting()).Bold(true).
			Render(truncate(
				"Needs "+string(managedAgent.Attention)+hint,
				width,
			))
	case managedAgent.ProcessLive &&
		managedAgent.Attention == agent.AttentionWaiting:
		meta = lipgloss.NewStyle().Foreground(colorWaiting()).
			Render(truncate("Unseen result", width))
	}
	heading := lipgloss.JoinVertical(lipgloss.Left, title, meta, "")

	viewportCopy := m.interaction
	composer := mutedStyle().Render(truncate("i reply  / search  Enter open terminal", width))
	if managedAgent.ProcessLive && managedAgent.Attention.Urgent() &&
		m.mode != modeCompose && m.mode != modeSearch {
		// The heading line is easy to slide past; the band above the
		// composer row is not. It replaces the input affordances entirely
		// when the agent's own terminal holds the prompt.
		//
		// A mark never suppresses this band. Marks are triage — how loudly a
		// row asks to be looked at — while the band says where text has to go
		// to reach the agent, which is a fact about the running prompt that
		// no reading of the state changes.
		guidance := " ⚠ Agent asked a question — i to reply, Enter for its terminal"
		if managedAgent.Attention.TerminalOwned() {
			guidance = " ⚠ Needs " + string(managedAgent.Attention) +
				" — Enter opens the terminal"
		}
		composer = attentionBandStyle().Width(width).Render(truncate(guidance, width))
	}
	if m.mode == modeSearch {
		m.search.input.SetWidth(max(1, width-2))
		composer = accentStyle().Render("/") + m.search.input.View()
	} else if m.search.query != "" {
		position := "no matches"
		if len(m.search.matches) > 0 {
			position = fmt.Sprintf(
				"match %d/%d",
				m.search.index+1,
				len(m.search.matches),
			)
		}
		composer = mutedStyle().Render(truncate(
			"/"+m.search.query+"  "+position+"  n next  N prev  Esc clear",
			width,
		))
	}
	if m.mode == modeCompose {
		m.sendInput.SetWidth(max(1, width))
		inputHeight := composerHeight(m.sendInput.Value(), max(1, width))
		m.sendInput.SetHeight(inputHeight)
		// The composer wears the wordmark palette mirrored: sapphire at
		// the edges rising to the crest at the center, light held between
		// two rules.
		rule := renderComposerRule(max(1, width))
		composer = lipgloss.JoinVertical(
			lipgloss.Left,
			rule,
			m.sendInput.View(),
			rule,
		)
		// The bordered composer grows; the transcript yields the rows.
		// Yield from the top: when the reader was at the bottom, the last
		// output must stay visible above the composer.
		atBottom := m.interaction.AtBottom()
		viewportCopy.SetHeight(max(1, viewportCopy.Height()-inputHeight-2+1))
		if atBottom {
			viewportCopy.GotoBottom()
		}
	}
	view := viewportCopy.View()
	if m.selectionActive {
		start, end := m.selectionRange()
		view = paintTranscriptSelection(view, viewportCopy.YOffset(), start, end)
	}
	transcript := view + ansi.ResetStyle
	if m.interactionID != managedAgent.ID {
		transcript = mutedStyle().Render(truncate("Loading interaction...", width))
	}
	return lipgloss.JoinVertical(
		lipgloss.Left,
		heading,
		transcript,
		composer,
	)
}

// interactionMeta renders the heading's subtitle. Outside Git it is one muted
// line — status tokens then path — truncated as a whole. In a Git workspace
// the checkout is named between the two: a path alone never says which of a
// workspace's checkouts an agent is editing, and saying nothing for the
// primary one leaves the reader unable to tell an unbadged agent from a
// dashboard that does not know. Either way the badge is the last token to
// yield width, and the path — which merely restates it — is the first.
// A metaToken is one fact in the Spanreed's masthead. Rendering and measuring
// are separate concerns because the styled string carries escape sequences no
// width calculation should see.
type metaToken struct {
	plain    string
	rendered string
}

func (t metaToken) width() int { return lipgloss.Width(t.plain) }

// metaSeparator groups the masthead's facts into readable tokens instead of
// letting them run together on spacing alone.
const metaSeparator = " · "

// interactionMeta is the masthead under the agent's name: what it is, what it
// is doing, what it may do unattended, which checkout it stands in, and where
// that is on disk. The state leads with the glyph the agent rows and the
// header counters use for it — the Spanreed is the fourth place that
// vocabulary appears, and the only one that used to spell the state as a bare
// grey word. Tokens drop from the tail when the pane narrows, so the loudest
// facts outlive the path.
func interactionMeta(managedAgent agent.Agent, width int) string {
	symbol, symbolStyle := statusVisual(managedAgent)
	tokens := []metaToken{{
		plain: symbol + " " + agentStateLabel(managedAgent),
		rendered: symbolStyle.Render(symbol) + " " +
			mutedStyle().Render(agentStateLabel(managedAgent)),
	}}
	if provider := string(managedAgent.Provider); provider != "" {
		tokens = append(tokens, metaToken{provider, mutedStyle().Render(provider)})
	}
	if badge := modeBadge(managedAgent.Mode); badge != "" {
		// AUTO is the one token here that changes what happens without you.
		// The dispatch modal has always said it in amber; saying it in grey
		// on the screen that stays open was the inconsistency.
		style := mutedStyle()
		if managedAgent.Mode == agent.ModeAuto {
			style = lipgloss.NewStyle().Foreground(colorWaiting()).Bold(true)
		}
		tokens = append(tokens, metaToken{badge, style.Render(badge)})
	}
	if checkout := agentCheckout(managedAgent); checkout.text != "" {
		tokens = append(tokens, metaToken{
			checkout.text,
			checkoutStyle(checkout).Render(checkout.text),
		})
	}
	if path := shortPath(managedAgent.Cwd); path != "" {
		tokens = append(tokens, metaToken{path, mutedStyle().Render(path)})
	}
	return joinMetaTokens(tokens, width)
}

func joinMetaTokens(tokens []metaToken, width int) string {
	separator := lipgloss.Width(metaSeparator)
	total := 0
	kept := 0
	for _, token := range tokens {
		cost := token.width()
		if kept > 0 {
			cost += separator
		}
		if total+cost > width {
			break
		}
		total += cost
		kept++
	}
	if kept == 0 {
		if len(tokens) == 0 {
			return ""
		}
		return truncate(tokens[0].plain, width)
	}
	parts := make([]string, 0, kept)
	for _, token := range tokens[:kept] {
		parts = append(parts, token.rendered)
	}
	return strings.Join(parts, mutedStyle().Render(metaSeparator))
}

// checkoutStyle picks the badge's color. A linked worktree is the tree worth
// naming loudly, so it keeps the accent; the primary checkout is stated in
// the muted tone the rest of the meta line uses, because in most repositories
// working there is simply where the work happens. Only this repository asks
// otherwise, and painting every workspace's normal case as a problem would be
// the dashboard shouting a house rule at people who never agreed to it. The
// alarm belongs to a per-workspace setting (#47); the fact belongs here.
func checkoutStyle(checkout checkoutBadge) lipgloss.Style {
	if checkout.primary {
		return mutedStyle()
	}
	return accentStyle()
}

func cleanInteraction(content string, width int, providerID agent.Provider) string {
	clean := sanitizeInteractionANSI(content)
	lines := focusConversation(strings.Split(clean, "\n"), providerID)
	lines = trimBlankInteractionLines(lines)
	if len(lines) == 0 {
		return mutedStyle().Render("No output yet")
	}

	compacted := make([]string, 0, len(lines))
	previousBlank := false
	for _, line := range lines {
		line = normalizeTerminalLine(line)
		wrapped := line
		if width > 0 {
			wrapped = wrapTranscriptLine(line, width)
		}
		wrappedLines := strings.Split(wrapped, "\n")
		// Wrapping cuts a styled run in half: the opening SGR sits on the
		// first row and the reset on the last. Every row is reopened with
		// the styling still in effect and closed again, because each row is
		// laid beside another pane — an unterminated color would run into
		// the neighbor, and a row that never reopened would lose its color.
		carried := ""
		for _, wrappedLine := range wrappedLines {
			wrappedLine = trimANSIRight(carried + wrappedLine)
			blank := strings.TrimSpace(ansi.Strip(wrappedLine)) == ""
			carried = activeSGR(wrappedLine)
			if carried != "" {
				wrappedLine += ansi.ResetStyle
			}
			if blank && previousBlank {
				continue
			}
			compacted = append(compacted, wrappedLine)
			previousBlank = blank
		}
	}
	compacted = trimBlankInteractionLines(compacted)
	if len(compacted) == 0 {
		return mutedStyle().Render("No output yet")
	}
	return strings.Join(compacted, "\n")
}

func normalizeTerminalLine(line string) string {
	trimmedANSI := trimANSIWhitespace(line)
	trimmed := ansi.Strip(trimmedANSI)
	if isTerminalFrameBorder(trimmed) {
		return ""
	}
	if strings.HasPrefix(trimmed, "│") && strings.HasSuffix(trimmed, "│") {
		width := ansi.StringWidth(trimmedANSI)
		if width <= 2 {
			return ""
		}
		interior := trimANSIRight(ansi.Cut(trimmedANSI, 1, width-1))
		// Frames pad content with one space after the border; anything
		// beyond that is the program's own indentation and must survive.
		if strings.HasPrefix(ansi.Strip(interior), " ") {
			interior = ansi.Cut(interior, 1, ansi.StringWidth(interior))
		}
		return interior
	}
	return trimANSIRight(line)
}

// wrapTranscriptLine wraps a transcript line to the pane width with a
// hanging indent: continuation rows inherit the line's leading whitespace,
// so indented agent output keeps its shape instead of snapping back to
// column zero every time the Spanreed pane is narrower than the agent's
// tmux pane.
func wrapTranscriptLine(line string, width int) string {
	stripped := ansi.Strip(line)
	if ansi.StringWidth(stripped) <= width {
		return line
	}
	indent := len(stripped) - len(strings.TrimLeft(stripped, " "))
	if indent == 0 || indent >= width/2 {
		return ansi.Wrap(line, width, " /")
	}
	rows := strings.Split(ansi.Wrap(line, width-indent, " /"), "\n")
	prefix := strings.Repeat(" ", indent)
	for index := 1; index < len(rows); index++ {
		rows[index] = prefix + rows[index]
	}
	return strings.Join(rows, "\n")
}

func sanitizeInteractionANSI(content string) string {
	content = strings.ReplaceAll(content, "\x00", "")
	var output strings.Builder
	var state byte
	for len(content) > 0 {
		sequence, cellWidth, consumed, nextState := ansi.DecodeSequence(content, state, nil)
		if consumed <= 0 {
			break
		}
		switch {
		case cellWidth > 0:
			output.WriteString(sequence)
		case sequence == "\n" || sequence == "\t":
			output.WriteString(sequence)
		case isPrintableZeroWidth(sequence):
			output.WriteString(sequence)
		case isSGRSequence(sequence):
			output.WriteString(sequence)
		}
		content = content[consumed:]
		state = nextState
	}
	return output.String()
}

func isPrintableZeroWidth(sequence string) bool {
	if sequence == "" {
		return false
	}
	first := sequence[0]
	if first >= utf8.RuneSelf {
		return utf8.ValidString(sequence)
	}
	return first >= 0x20 && first != 0x7f && first != 0x1b
}

func isSGRSequence(sequence string) bool {
	return len(sequence) >= 3 &&
		strings.HasPrefix(sequence, "\x1b[") &&
		strings.HasSuffix(sequence, "m")
}

// activeSGR returns the styling a line leaves switched on: every SGR
// sequence since the last reset, in order, ready to be replayed at the head
// of the row that continues it.
func activeSGR(line string) string {
	var active []string
	var state byte
	for len(line) > 0 {
		sequence, _, consumed, next := ansi.DecodeSequence(line, state, nil)
		if consumed <= 0 {
			break
		}
		if isSGRSequence(sequence) {
			if opensWithReset(sequence) {
				active = active[:0]
			}
			if !isResetSGR(sequence) {
				active = append(active, sequence)
			}
		}
		line = line[consumed:]
		state = next
	}
	return strings.Join(active, "")
}

// sgrParameters returns an SGR sequence's parameters: "\x1b[1;31m" → 1, 31.
// A bare "\x1b[m" is the implicit reset, so it yields one empty parameter.
func sgrParameters(sequence string) []string {
	return strings.Split(sequence[2:len(sequence)-1], ";")
}

// opensWithReset reports whether a sequence starts by clearing all styling,
// which "\x1b[0;31m" does before setting red.
func opensWithReset(sequence string) bool {
	first := sgrParameters(sequence)[0]
	return first == "" || strings.TrimLeft(first, "0") == ""
}

func isResetSGR(sequence string) bool {
	for _, parameter := range sgrParameters(sequence) {
		if parameter != "" && strings.TrimLeft(parameter, "0") != "" {
			return false
		}
	}
	return true
}

func focusConversation(lines []string, providerID agent.Provider) []string {
	marker := ""
	switch providerID {
	case agent.ProviderClaude:
		marker = "❯"
	case agent.ProviderCodex:
		marker = "›"
	default:
		return lines
	}

	lines = trimBlankInteractionLines(lines)
	lines = trimTerminalComposer(lines, marker)
	for index, line := range lines {
		if promptHasContent(line, marker) {
			return lines[index:]
		}
	}
	return lines
}

func trimTerminalComposer(lines []string, marker string) []string {
	searchStart := max(0, len(lines)-10)
	for index := len(lines) - 1; index >= searchStart; index-- {
		text := strings.TrimSpace(ansi.Strip(lines[index]))
		if !strings.HasPrefix(text, marker) {
			continue
		}
		empty := strings.TrimSpace(strings.TrimPrefix(text, marker)) == ""
		if !empty && !composerStatusFollows(lines[index+1:]) {
			continue
		}
		cut := index
		for previous := index - 1; previous >= searchStart; previous-- {
			text := strings.TrimSpace(ansi.Strip(lines[previous]))
			if text == "" {
				continue
			}
			if isTerminalDivider(text) {
				cut = previous
			}
			break
		}
		return lines[:cut]
	}
	return lines
}

func composerStatusFollows(lines []string) bool {
	for _, line := range lines {
		text := strings.TrimSpace(ansi.Strip(line))
		if text == "" {
			continue
		}
		return isTerminalDivider(text) ||
			(strings.Contains(text, " · ") && strings.Contains(text, "/"))
	}
	return false
}

func promptHasContent(line, marker string) bool {
	text := strings.TrimSpace(ansi.Strip(line))
	if !strings.HasPrefix(text, marker) {
		return false
	}
	return strings.TrimSpace(strings.TrimPrefix(text, marker)) != ""
}

func isTerminalDivider(line string) bool {
	total := 0
	horizontal := 0
	for _, character := range line {
		if unicode.IsSpace(character) {
			continue
		}
		total++
		switch character {
		case '─', '━', '═', '╌', '-':
			horizontal++
		}
	}
	return horizontal >= 8 && horizontal*2 >= total
}

func trimBlankInteractionLines(lines []string) []string {
	for len(lines) > 0 && strings.TrimSpace(ansi.Strip(lines[0])) == "" {
		lines = lines[1:]
	}
	for len(lines) > 0 && strings.TrimSpace(ansi.Strip(lines[len(lines)-1])) == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

func trimANSIWhitespace(line string) string {
	plain := ansi.Strip(line)
	leftTrimmed := strings.TrimLeftFunc(plain, unicode.IsSpace)
	if leftTrimmed == "" {
		return ""
	}
	trimmed := strings.TrimRightFunc(leftTrimmed, unicode.IsSpace)
	leftWidth := ansi.StringWidth(plain[:len(plain)-len(leftTrimmed)])
	contentWidth := ansi.StringWidth(trimmed)
	return ansi.Cut(line, leftWidth, leftWidth+contentWidth)
}

func trimANSIRight(line string) string {
	plain := ansi.Strip(line)
	trimmed := strings.TrimRightFunc(plain, unicode.IsSpace)
	if trimmed == "" {
		return ""
	}
	return ansi.Truncate(line, ansi.StringWidth(trimmed), "")
}

func isTerminalFrameBorder(line string) bool {
	if line == "" {
		return false
	}
	for _, char := range line {
		switch char {
		case '╭', '╮', '╰', '╯', '┌', '┐', '└', '┘', '─', '━', '╌', '═':
		default:
			return false
		}
	}
	return true
}
