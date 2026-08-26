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

// StormGlyph is Stormlight's mark: Nerd Font U+F067E,
// nf-md-weather_lightning_rainy. It opens the wordmark, caps the
// footer, and leads the oath printed on the way out — every place the
// program signs its own name. A four-pointed star stood here before, and
// said only "something bright".
//
// Exported for that last one: the farewell is printed by main after the
// dashboard is gone, and one mark spelled in two places is one that drifts.
//
// It is a Private Use Area codepoint, which is a harder bargain than the
// rest of the dashboard drives. The header is the first thing painted and
// the last thing anyone would think to blame, so in a terminal without a
// Nerd Font the identity line opens with an empty box. Nothing can ask a
// terminal whether it has the glyph, so this is a requirement rather than
// something to detect and work around, and README says so.
//
// One cell wide — in lipgloss, and in a real terminal, which is what the
// header's gap arithmetic assumes when it places the counters at the far
// edge. It sits in Plane 15 rather than the Basic Multilingual Plane's
// private area, which changes nothing about that: both are Private Use,
// both measure one column.
//
// It comes from the Material Design set rather than the Weather Icons set
// the mark started in, and the reason is size. Patched into a Mono
// variant, every glyph is squeezed into one cell, and the two sets do not
// arrive there the same: measured in JetBrains Mono Nerd Font Mono, the
// weather icon inked 446x462 against a capital M's 456x730, while this one
// fills 600x544. Same column, half again the ink, which is the only sense
// in which a terminal glyph can be made bigger — the cell belongs to the
// font, not to us.
const StormGlyph = "\U000f067e"

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
	out.WriteString(glint.Render(StormGlyph + " "))
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
		// The cursor mark speaks the strip's silver — selection is one
		// word everywhere — leaving amber to mean attention alone.
		focusMark: colorBand(),
		restMark:  colorBorder(),
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
