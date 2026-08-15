// Package theme holds Stormlight's palette.
//
// The colors live here rather than inside internal/ui because the Claude
// transcript renderer paints its own output: only the renderer knows which
// run of text is a prompt, a tool call, or a trimmed result, and rebuilding
// that structure downstream from markers would be guesswork.
package theme

import (
	"image/color"
	"sync/atomic"

	"charm.land/lipgloss/v2"
)

// Pair is a palette entry: the same role painted for a light terminal and
// for a dark one.
//
// Lip Gloss v1 carried this distinction itself as an AdaptiveColor and
// resolved it at render time, querying the terminal behind the program's
// back. v2 removed that — a color is a color, and choosing between variants
// is the program's job — so the pair stays unresolved here until Color or
// Hex is asked for one, against the background Resolve recorded.
type Pair struct{ Light, Dark string }

var (
	// Accent is the one entry that does not vary: it is the wordmark's
	// blue, chosen to sit legibly on either background, and it stays a
	// Pair only so every palette entry resolves the same way.
	Accent       = Pair{Light: "#62AEEF", Dark: "#62AEEF"}
	Text         = Pair{Light: "#24323A", Dark: "#D7DEE5"}
	Muted        = Pair{Light: "#70808A", Dark: "#74818B"}
	Working      = Pair{Light: "#26799D", Dark: "#61AFEF"}
	Waiting      = Pair{Light: "#A86600", Dark: "#E5C07B"}
	Done         = Pair{Light: "#257A4A", Dark: "#72C087"}
	Failed       = Pair{Light: "#B33838", Dark: "#E06C75"}
	Border       = Pair{Light: "#AAB3B9", Dark: "#59636B"}
	Select       = Pair{Light: "#E1E4E6", Dark: "#3D4245"}
	SelectedText = Pair{Light: "#172027", Dark: "#F3F5F6"}
	DangerBg     = Pair{Light: "#F2D5D1", Dark: "#552B29"}
	// Code tints literals and fenced blocks in rendered transcripts — a
	// teal drawn from the wordmark's cool end so code reads as a different
	// substance than prose without competing with the status colors.
	Code = Pair{Light: "#0E7C6B", Dark: "#8FDCCB"}
	// Recede is chrome stepping back: the pane seams while the Spanreed
	// terminal holds the keyboard, receded so the portal is the subject.
	Recede = Pair{Light: "#D6DCE0", Dark: "#383E44"}
	// PortalInk is the window bar's text on its Accent-filled band while
	// the terminal holds the keyboard. The Accent is identical on both
	// grounds, so its ink is too.
	PortalInk = Pair{Light: "#0F2338", Dark: "#0F2338"}
	// Band is the title strip's ground: the wordmark gradient's palest
	// stop, so the strip and the Stormlight header are cut from the same
	// light. Carries PortalInk the same as the Accent does.
	Band = Pair{Light: "#BFDEDA", Dark: "#C8F7EF"}
	// BandMuted is the roster segment the cursor is not in: distinctly
	// below Band in the same hue, so the selected pane's segment reads
	// brighter at a glance without breaking the shared surface.
	BandMuted = Pair{Light: "#DAEBE8", Dark: "#8FB6AF"}
	// BandDim is the same surface receded: the roster half of the strip
	// steps back while the keyboard lives in the portal, so the lit
	// band always means "you are on this side of the seam".
	BandDim = Pair{Light: "#EAF2F0", Dark: "#5C6F6B"}
)

// dark records which background the palette resolves against.
//
// It is atomic because the two readers do not share a goroutine: the
// dashboard resolves palette entries while rendering on Bubble Tea's loop,
// and the transcript renderer resolves them inside the tea.Cmd goroutine
// that turns a captured pane into markdown.
//
// The default is dark rather than light because a terminal that never
// answers the background query is far more likely to be dark, so the frames
// drawn before the answer arrives guess right in the common case.
var dark atomic.Bool

func init() { dark.Store(true) }

// Resolve records the terminal background the palette should paint for.
//
// The dashboard calls this when Bubble Tea answers RequestBackgroundColor.
// Because every style is built by a function rather than held in a package
// variable, entries resolved before the answer arrives are not cached — the
// next frame simply paints with the corrected palette.
func Resolve(isDark bool) { dark.Store(isDark) }

// Dark reports whether the palette is resolving against a dark terminal.
func Dark() bool { return dark.Load() }

// Color resolves a palette entry for rendering.
func Color(p Pair) color.Color { return lipgloss.Color(Hex(p)) }

// Hex resolves a palette entry to its literal hex string.
//
// A markdown stylesheet is not rendered through Lip Gloss — it is a data
// structure of color strings handed to the markdown renderer before any
// output exists — so it needs the string rather than the color.
func Hex(p Pair) string {
	if Dark() {
		return p.Dark
	}
	return p.Light
}
