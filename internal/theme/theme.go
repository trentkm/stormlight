// Package theme holds Stormlight's palette.
//
// The colors live here rather than inside internal/ui because the Claude
// transcript renderer paints its own output: only the renderer knows which
// run of text is a prompt, a tool call, or a trimmed result, and rebuilding
// that structure downstream from markers would be guesswork.
package theme

import "github.com/charmbracelet/lipgloss"

var (
	Accent       = lipgloss.Color("#62AEEF")
	Text         = lipgloss.AdaptiveColor{Light: "#24323A", Dark: "#D7DEE5"}
	Muted        = lipgloss.AdaptiveColor{Light: "#70808A", Dark: "#74818B"}
	Working      = lipgloss.AdaptiveColor{Light: "#26799D", Dark: "#61AFEF"}
	Waiting      = lipgloss.AdaptiveColor{Light: "#A86600", Dark: "#E5C07B"}
	Done         = lipgloss.AdaptiveColor{Light: "#257A4A", Dark: "#72C087"}
	Failed       = lipgloss.AdaptiveColor{Light: "#B33838", Dark: "#E06C75"}
	Border       = lipgloss.AdaptiveColor{Light: "#AAB3B9", Dark: "#59636B"}
	Select       = lipgloss.AdaptiveColor{Light: "#E1E4E6", Dark: "#3D4245"}
	SelectedText = lipgloss.AdaptiveColor{Light: "#172027", Dark: "#F3F5F6"}
	DangerBg     = lipgloss.AdaptiveColor{Light: "#F2D5D1", Dark: "#552B29"}
	// Code tints literals and fenced blocks in rendered transcripts — a
	// teal drawn from the wordmark's cool end so code reads as a different
	// substance than prose without competing with the status colors.
	Code = lipgloss.AdaptiveColor{Light: "#0E7C6B", Dark: "#8FDCCB"}
)
