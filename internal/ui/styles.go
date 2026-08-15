package ui

// Colors, styles, the wordmark, and the stormlight shimmer.
// Split from model.go; see #34.

import (
	"fmt"
	"image/color"
	"strconv"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/trentkm/stormlight/internal/theme"
)

// The palette lives in internal/theme so the transcript renderer can paint
// with the same colors; these are the names the dashboard reads by.
//
// They are functions rather than variables because a palette entry cannot
// resolve until the terminal has answered which background it draws on, and
// that answer arrives as a message well after package initialization. A
// variable would capture the guess made before the answer; a function
// re-resolves on every frame, so the first frame after the answer lands is
// already painted correctly.
func colorAccent() color.Color       { return theme.Color(theme.Accent) }
func colorText() color.Color         { return theme.Color(theme.Text) }
func colorMuted() color.Color        { return theme.Color(theme.Muted) }
func colorWorking() color.Color      { return theme.Color(theme.Working) }
func colorWaiting() color.Color      { return theme.Color(theme.Waiting) }
func colorDone() color.Color         { return theme.Color(theme.Done) }
func colorFailed() color.Color       { return theme.Color(theme.Failed) }
func colorBorder() color.Color       { return theme.Color(theme.Border) }
func colorSelect() color.Color       { return theme.Color(theme.Select) }
func colorSelectedText() color.Color { return theme.Color(theme.SelectedText) }
func colorDangerBg() color.Color     { return theme.Color(theme.DangerBg) }
func colorRecede() color.Color       { return theme.Color(theme.Recede) }
func colorPortalInk() color.Color    { return theme.Color(theme.PortalInk) }
func colorBand() color.Color         { return theme.Color(theme.Band) }
func colorBandMuted() color.Color    { return theme.Color(theme.BandMuted) }
func colorBandDim() color.Color      { return theme.Color(theme.BandDim) }
func colorAccentDim() color.Color    { return theme.Color(theme.AccentDim) }

func titleStyle() lipgloss.Style {
	return lipgloss.NewStyle().Bold(true).Foreground(colorText())
}

// attentionBandInk is the band's own ink rather than a shared palette entry:
// it is picked to sit on the amber ground, not on the terminal's background.
var attentionBandInk = theme.Pair{Light: "#FFF6E5", Dark: "#1F2328"}

// attentionBandStyle() is the unmissable full-width bar for an agent blocked
// on human input — amber ground, dark text, no subtlety.
func attentionBandStyle() lipgloss.Style {
	return lipgloss.NewStyle().
		Bold(true).
		Background(colorWaiting()).
		Foreground(theme.Color(attentionBandInk))
}

func mutedStyle() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(colorMuted())
}

func accentStyle() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(colorAccent()).Bold(true)
}

func errorStyle() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(colorFailed())
}

func successStyle() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(colorDone())
}

// Working things glow: a brighter band sweeps across their text — the
// closest a terminal gets to holding stormlight. stormlightGlow orders the
// shades base → mid → bright → crest; the crest sits at the band's center
// and falls off on both sides. The sweep replaces blinking as the working
// indicator for the header, workspace names, and agent titles.
const stormlightTitle = "Stormlight"

// shimmerRest adds off-screen travel on both ends of each sweep so the glow
// rests at the base shade between passes instead of wrapping abruptly.
const shimmerRest = 14

var stormlightGlow = []theme.Pair{
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

func shimmerText(text string, phase int, background color.Color) string {
	return shimmerTextWith(stormlightGlow, text, phase, background)
}

// The wordmark holds light: each letter takes a fixed sapphire→sky→ice
// gradient, and while agents work the sweep blends letters toward the
// crest color as it passes — a storm moving through the word.
var (
	wordmarkStopsDark  = [3]string{"#7AA2F7", "#7DCFFF", "#C8F7EF"}
	wordmarkStopsLight = [3]string{"#2450A8", "#0E6FA8", "#0D8A80"}
	wordmarkCrest      = theme.Pair{Light: "#001B4D", Dark: "#FFFFFF"}
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
		Foreground(theme.Color(theme.Pair{
			Light: wordmarkStopsLight[1],
			Dark:  wordmarkStopsDark[1],
		}))
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
			Foreground(theme.Color(theme.Pair{Light: light, Dark: dark})).
			Render(string(letter)))
	}
	return out.String()
}

func shimmerTextWith(
	glow []theme.Pair,
	text string,
	phase int,
	background color.Color,
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
			Foreground(theme.Color(glow[shade]))
		if background != nil {
			style = style.Background(background)
		}
		out.WriteString(style.Render(string(letter)))
	}
	return out.String()
}

// rowTheme colors a selected list row. selectTheme() is the normal selection;
// dangerTheme() marks a row awaiting delete confirmation.
type rowTheme struct {
	background color.Color
	text       color.Color
	focusMark  color.Color
	restMark   color.Color
}

func selectTheme() rowTheme {
	return rowTheme{
		background: colorSelect(),
		text:       colorSelectedText(),
		focusMark:  colorWaiting(),
		restMark:   colorBorder(),
	}
}

func dangerTheme() rowTheme {
	return rowTheme{
		background: colorDangerBg(),
		text:       colorSelectedText(),
		focusMark:  colorFailed(),
		restMark:   colorFailed(),
	}
}

func rowThemeFor(danger bool) rowTheme {
	if danger {
		return dangerTheme()
	}
	return selectTheme()
}
