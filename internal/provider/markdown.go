package provider

import (
	"strings"
	"sync"

	"charm.land/glamour/v2"
	"charm.land/glamour/v2/ansi"
	"charm.land/lipgloss/v2"
	"github.com/trentkm/stormlight/internal/theme"
)

// Claude writes markdown, so the transcript renderer reads it back rather
// than showing the syntax. Doing that by hand covered the two constructs
// somebody thought of — ** and ` — and left every other one leaking its
// delimiters into the pane, which is an open-ended supply of bugs: emphasis,
// links, strikethrough, tables and nested lists all render as their own
// source text. Glamour parses the real grammar instead, and its stylesheet
// is built here from internal/theme so the pane still lights from Stormlight's
// palette rather than Glamour's.
//
// Width is deliberately not Glamour's job. The Spanreed pane is resizable,
// so wrapping has to happen at paint time against the current column width —
// which the stylesheet, built once, cannot know. WithWordWrap(0) turns
// Glamour's wrapping off and leaves each block one long line for
// wrapTranscriptLine to break. Any non-zero value would also right-pad every
// row to the wrap width with styled spaces, which the pane then has to strip.

var (
	// markdownMu serializes Render and guards the cached renderer below. A
	// TermRenderer carries one render context with a block stack across
	// calls, so two transcripts rendered at once would interleave their
	// blocks.
	markdownMu sync.Mutex
	// markdownRenderer is the cached renderer and markdownDark is the
	// background its stylesheet was built for.
	//
	// The stylesheet is a data structure of resolved color strings, so it
	// is only correct for one background. Caching it outright — as a
	// sync.OnceValue did — was safe only while Lip Gloss v1 answered the
	// background question synchronously on first use. The answer now
	// arrives as a message, so a transcript rendered before it lands would
	// otherwise pin the guessed palette for the rest of the session.
	// Keying the cache on the background rebuilds it exactly once, when
	// the answer changes it.
	markdownRenderer *glamour.TermRenderer
	markdownDark     bool
	markdownBuilt    bool
)

// proseStyle paints prose when markdown rendering is unavailable.
func proseStyle() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(theme.Color(theme.Text))
}

// cachedRenderer returns the renderer for the current background, building
// it if the background has changed since the last call. Callers must hold
// markdownMu.
func cachedRenderer() *glamour.TermRenderer {
	if dark := theme.Dark(); !markdownBuilt || dark != markdownDark {
		markdownRenderer = newMarkdownRenderer()
		markdownDark = dark
		markdownBuilt = true
	}
	return markdownRenderer
}

func newMarkdownRenderer() *glamour.TermRenderer {
	renderer, err := glamour.NewTermRenderer(
		glamour.WithStyles(markdownStyle()),
		glamour.WithWordWrap(0),
		glamour.WithPreservedNewLines(),
		// Chroma defaults to a 256-color formatter, which rounds the palette
		// to the nearest xterm index and leaves fenced code a shade off the
		// truecolor prose around it. Lipgloss downsamples for terminals that
		// need it; the stylesheet should not do it twice.
		glamour.WithChromaFormatter("terminal16m"),
	)
	if err != nil {
		return nil
	}
	return renderer
}

// renderMarkdown paints one assistant message. It falls back to flat prose
// styling rather than failing: a transcript that renders is worth more than
// one that reports why it could not.
func renderMarkdown(text string) string {
	markdownMu.Lock()
	renderer := cachedRenderer()
	if renderer == nil {
		markdownMu.Unlock()
		return paintLines(text, proseStyle())
	}
	rendered, err := renderer.Render(text)
	markdownMu.Unlock()
	if err != nil {
		return paintLines(text, proseStyle())
	}
	// Glamour brackets a document with blank lines. The caller lays the ⏺
	// marker on the first row, which has to be text.
	return strings.Trim(rendered, "\n")
}

// paintLines styles each line separately so every rendered row carries — and
// closes — its own styling, which is what the Spanreed pane needs to wrap and
// highlight rows independently.
func paintLines(text string, style lipgloss.Style) string {
	lines := strings.Split(text, "\n")
	for index, line := range lines {
		lines[index] = style.Render(line)
	}
	return strings.Join(lines, "\n")
}

func color(c theme.Pair) *string {
	hex := theme.Hex(c)
	return &hex
}

func flag(value bool) *bool { return &value }

func size(value uint) *uint { return &value }

func token(value string) *string { return &value }

// markdownStyle is Stormlight's markdown stylesheet. It starts from the zero
// StyleConfig rather than one of Glamour's presets, so nothing is inherited
// unseen: every prefix, indent and color in the pane is stated here.
func markdownStyle() ansi.StyleConfig {
	return ansi.StyleConfig{
		Document: ansi.StyleBlock{
			StylePrimitive: ansi.StylePrimitive{Color: color(theme.Text)},
			// The pane owns the left gutter: writeEntry lays the marker on
			// the first row and indents the rest under it.
			Margin: size(0),
		},
		Paragraph: ansi.StyleBlock{},
		// Text stays uncolored on purpose. It applies to the inline text of
		// every block, including a heading's, so giving it a color here
		// silently overrides the block that contains it — headings included.
		// Prose takes its color from Document instead.
		Text: ansi.StylePrimitive{},

		// Headings take the accent and nothing else. Glamour's presets hang
		// a "#" prefix and a background band on H1; in a pane this narrow
		// that reads as a tool result, not a heading.
		Heading: ansi.StyleBlock{
			StylePrimitive: ansi.StylePrimitive{
				Color: color(theme.Accent),
				Bold:  flag(true),
			},
		},

		Emph:          ansi.StylePrimitive{Italic: flag(true)},
		Strong:        ansi.StylePrimitive{Bold: flag(true)},
		Strikethrough: ansi.StylePrimitive{CrossedOut: flag(true)},

		List:        ansi.StyleList{LevelIndent: 2},
		Item:        ansi.StylePrimitive{BlockPrefix: "• "},
		Enumeration: ansi.StylePrimitive{BlockPrefix: ". "},
		Task: ansi.StyleTask{
			Ticked:   "[x] ",
			Unticked: "[ ] ",
		},

		BlockQuote: ansi.StyleBlock{
			StylePrimitive: ansi.StylePrimitive{Color: color(theme.Muted)},
			Indent:         size(1),
			IndentToken:    token("│ "),
		},
		HorizontalRule: ansi.StylePrimitive{
			Color:  color(theme.Border),
			Format: "\n────────\n",
		},

		Link:     ansi.StylePrimitive{Color: color(theme.Accent), Underline: flag(true)},
		LinkText: ansi.StylePrimitive{Color: color(theme.Working)},

		// Literals keep the tint they had before Glamour: code reads as a
		// different substance than prose. Glamour's presets pad inline code
		// with spaces and a background chip, which doubles every backtick's
		// width — the tint alone already says "literal".
		Code: ansi.StyleBlock{
			StylePrimitive: ansi.StylePrimitive{Color: color(theme.Code)},
		},
		CodeBlock: ansi.StyleCodeBlock{
			StyleBlock: ansi.StyleBlock{
				StylePrimitive: ansi.StylePrimitive{Color: color(theme.Code)},
				Margin:         size(2),
			},
			Chroma: syntaxColors(),
		},

		Table: ansi.StyleTable{
			StyleBlock:      ansi.StyleBlock{StylePrimitive: ansi.StylePrimitive{Color: color(theme.Text)}},
			CenterSeparator: token("┼"),
			ColumnSeparator: token("│"),
			RowSeparator:    token("─"),
		},
	}
}

// syntaxColors maps Chroma's token classes onto the dashboard palette.
// Nothing new is invented for syntax highlighting: a fenced block stays
// teal-dominant so it still reads as code at a glance, and the handful of
// classes worth separating borrow the status colors already on screen.
func syntaxColors() *ansi.Chroma {
	return &ansi.Chroma{
		Text:                ansi.StylePrimitive{Color: color(theme.Code)},
		Error:               ansi.StylePrimitive{Color: color(theme.Failed)},
		Comment:             ansi.StylePrimitive{Color: color(theme.Muted)},
		CommentPreproc:      ansi.StylePrimitive{Color: color(theme.Muted)},
		Keyword:             ansi.StylePrimitive{Color: color(theme.Accent)},
		KeywordReserved:     ansi.StylePrimitive{Color: color(theme.Accent)},
		KeywordNamespace:    ansi.StylePrimitive{Color: color(theme.Accent)},
		KeywordType:         ansi.StylePrimitive{Color: color(theme.Accent)},
		Operator:            ansi.StylePrimitive{Color: color(theme.Code)},
		Punctuation:         ansi.StylePrimitive{Color: color(theme.Code)},
		Name:                ansi.StylePrimitive{Color: color(theme.Code)},
		NameBuiltin:         ansi.StylePrimitive{Color: color(theme.Working)},
		NameClass:           ansi.StylePrimitive{Color: color(theme.Working)},
		NameFunction:        ansi.StylePrimitive{Color: color(theme.Working)},
		NameTag:             ansi.StylePrimitive{Color: color(theme.Accent)},
		NameAttribute:       ansi.StylePrimitive{Color: color(theme.Working)},
		Literal:             ansi.StylePrimitive{Color: color(theme.Done)},
		LiteralString:       ansi.StylePrimitive{Color: color(theme.Done)},
		LiteralStringEscape: ansi.StylePrimitive{Color: color(theme.Waiting)},
		LiteralNumber:       ansi.StylePrimitive{Color: color(theme.Waiting)},
		LiteralDate:         ansi.StylePrimitive{Color: color(theme.Waiting)},
		GenericDeleted:      ansi.StylePrimitive{Color: color(theme.Failed)},
		GenericInserted:     ansi.StylePrimitive{Color: color(theme.Done)},
		GenericEmph:         ansi.StylePrimitive{Italic: flag(true)},
		GenericStrong:       ansi.StylePrimitive{Bold: flag(true)},
		GenericSubheading:   ansi.StylePrimitive{Color: color(theme.Accent)},
	}
}
