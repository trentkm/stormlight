package ui

// Colors, styles, the wordmark, and the stormlight shimmer.
// Split from model.go; see #34.

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/trentkm/stormlight/internal/theme"
)

// The palette lives in internal/theme so the transcript renderer can paint
// with the same colors; these are the names the dashboard reads by.
var (
	colorAccent       = theme.Accent
	colorText         = theme.Text
	colorMuted        = theme.Muted
	colorWorking      = theme.Working
	colorWaiting      = theme.Waiting
	colorDone         = theme.Done
	colorFailed       = theme.Failed
	colorBorder       = theme.Border
	colorSelect       = theme.Select
	colorSelectedText = theme.SelectedText
	colorDangerBg     = theme.DangerBg

	titleStyle = lipgloss.NewStyle().Bold(true).Foreground(colorText)
	// attentionBandStyle is the unmissable full-width bar for an agent
	// blocked on human input — amber ground, dark text, no subtlety.
	attentionBandStyle = lipgloss.NewStyle().
				Bold(true).
				Background(colorWaiting).
				Foreground(lipgloss.AdaptiveColor{Light: "#FFF6E5", Dark: "#1F2328"})
	mutedStyle   = lipgloss.NewStyle().Foreground(colorMuted)
	accentStyle  = lipgloss.NewStyle().Foreground(colorAccent).Bold(true)
	errorStyle   = lipgloss.NewStyle().Foreground(colorFailed)
	successStyle = lipgloss.NewStyle().Foreground(colorDone)
)

// Working things glow: a brighter band sweeps across their text — the
// closest a terminal gets to holding stormlight. stormlightGlow orders the
// shades base → mid → bright → crest; the crest sits at the band's center
// and falls off on both sides. The sweep replaces blinking as the working
// indicator for the header, workspace names, and agent titles.
const stormlightTitle = "Stormlight"

// shimmerRest adds off-screen travel on both ends of each sweep so the glow
// rests at the base shade between passes instead of wrapping abruptly.
const shimmerRest = 14

var stormlightGlow = []lipgloss.AdaptiveColor{
	{Light: "#0F7A90", Dark: "#3BA8BD"},
	{Light: "#0A93AE", Dark: "#5CC6DB"},
	{Light: "#00A9C9", Dark: "#8AE7F8"},
	{Light: "#00C2E8", Dark: "#C4F5FF"},
}

// shimmerText renders text in the glow palette. A negative phase (or a
// resting band position) yields the uniform base shade; otherwise the
// bright band centers on one rune and sweeps as the phase advances.
// background, when non-nil, preserves row highlighting behind the glow.
// shimmerBand computes the crest position for a text of the given length; a
// negative phase parks the band off-text so everything renders at base.
func shimmerBand(length, phase int) int {
	if phase < 0 {
		return -shimmerRest
	}
	return phase%(length+shimmerRest) - 4
}

func shimmerText(text string, phase int, background lipgloss.TerminalColor) string {
	return shimmerTextWith(stormlightGlow, text, phase, background)
}

// The wordmark holds light: each letter takes a fixed sapphire→sky→ice
// gradient, and while agents work the sweep blends letters toward the
// crest color as it passes — a storm moving through the word.
var (
	wordmarkStopsDark  = [3]string{"#7AA2F7", "#7DCFFF", "#C8F7EF"}
	wordmarkStopsLight = [3]string{"#2450A8", "#0E6FA8", "#0D8A80"}
	wordmarkCrest      = lipgloss.AdaptiveColor{Light: "#001B4D", Dark: "#FFFFFF"}
)

func hexChannel(hex string, index int) int {
	value, err := strconv.ParseInt(hex[1+index*2:3+index*2], 16, 32)
	if err != nil {
		return 0
	}
	return int(value)
}

func lerpHex(from, to string, t float64) string {
	blend := func(index int) int {
		a, b := hexChannel(from, index), hexChannel(to, index)
		return a + int(float64(b-a)*t)
	}
	return fmt.Sprintf("#%02X%02X%02X", blend(0), blend(1), blend(2))
}

func gradientStop(stops [3]string, t float64) string {
	if t <= 0.5 {
		return lerpHex(stops[0], stops[1], t*2)
	}
	return lerpHex(stops[1], stops[2], (t-0.5)*2)
}

// renderWordmark paints the title's gradient and, while the shimmer runs,
// brightens letters toward the crest as the band passes them.
func renderWordmark(phase int) string {
	runes := []rune(stormlightTitle)
	band := shimmerBand(len(runes), phase)
	var out strings.Builder
	glint := lipgloss.NewStyle().
		Foreground(lipgloss.AdaptiveColor{
			Light: wordmarkStopsLight[1],
			Dark:  wordmarkStopsDark[1],
		})
	out.WriteString(glint.Render("✦ "))
	for index, letter := range runes {
		t := 0.0
		if len(runes) > 1 {
			t = float64(index) / float64(len(runes)-1)
		}
		dark := gradientStop(wordmarkStopsDark, t)
		light := gradientStop(wordmarkStopsLight, t)
		distance := index - band
		if distance < 0 {
			distance = -distance
		}
		if weight := [4]float64{0.85, 0.55, 0.25, 0}[min(distance, 3)]; weight > 0 {
			dark = lerpHex(dark, wordmarkCrest.Dark, weight)
			light = lerpHex(light, wordmarkCrest.Light, weight)
		}
		out.WriteString(lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.AdaptiveColor{Light: light, Dark: dark}).
			Render(string(letter)))
	}
	return out.String()
}

func shimmerTextWith(
	glow []lipgloss.AdaptiveColor,
	text string,
	phase int,
	background lipgloss.TerminalColor,
) string {
	runes := []rune(text)
	band := shimmerBand(len(runes), phase)
	var out strings.Builder
	for index, letter := range runes {
		distance := index - band
		if distance < 0 {
			distance = -distance
		}
		shade := max(0, len(glow)-1-distance)
		style := lipgloss.NewStyle().
			Bold(true).
			Foreground(glow[shade])
		if background != nil {
			style = style.Background(background)
		}
		out.WriteString(style.Render(string(letter)))
	}
	return out.String()
}

// rowTheme colors a selected list row. selectTheme is the normal selection;
// dangerTheme marks a row awaiting delete confirmation.
type rowTheme struct {
	background lipgloss.TerminalColor
	text       lipgloss.TerminalColor
	focusMark  lipgloss.TerminalColor
	restMark   lipgloss.TerminalColor
}

var (
	selectTheme = rowTheme{
		background: colorSelect,
		text:       colorSelectedText,
		focusMark:  colorWaiting,
		restMark:   colorBorder,
	}
	dangerTheme = rowTheme{
		background: colorDangerBg,
		text:       colorSelectedText,
		focusMark:  colorFailed,
		restMark:   colorFailed,
	}
)

func rowThemeFor(danger bool) rowTheme {
	if danger {
		return dangerTheme
	}
	return selectTheme
}
