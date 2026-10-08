package ui

// The sky: what the terminal pane draws when there is no terminal.
//
// Two states draw into a pane built for a terminal and have nothing to
// put there: no agent selected, and an agent whose terminal is still
// opening. Each used to be one muted line. Now each is the Rosharan
// system seen from above — the sun at the center, and around it Ashyn
// close and burning, Roshar with its moon, and Braize cold and far —
// over a field of particles that glimmer in from nothing, swell, and
// fade out, each on its own clock. Now and then a meteor. The caption
// sits beneath it all, as it did.
//
// Everything but the sun is a point of light, which is the one thing a
// cell grid draws without compromise: an orbit is a ring of sparse dots,
// a planet one glyph, and nothing has to survive the 2:1 aspect except
// the orbits themselves, which are drawn twice as wide as they are tall
// so they read as circles. Every glyph is one cell wide, so nothing
// shears. The field is generated from fixed hashes rather than random
// numbers, so a frame is a pure function of its phase: the same
// particles glimmer in the same places every time, tests can pin a
// frame, and nothing fizzes.

import (
	"fmt"
	"image/color"
	"math"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/trentkm/stormlight/internal/theme"
)

// skyHash scrambles three integers into a well-mixed non-negative one.
func skyHash(a, b, c int) int {
	h := a*2654435761 ^ b*40503 ^ c*97
	h ^= h >> 13
	h *= 1274126177
	h ^= h >> 16
	if h < 0 {
		h = -h
	}
	return h
}

// A particle's brightness, from the faintest point to a flare.
type starLevel int

const (
	starDark starLevel = iota
	starFaint
	starSoft
	starBright
	starFlare
)

var starGlyphs = [...]rune{starDark: ' ', starFaint: '·', starSoft: '•', starBright: '✦', starFlare: '✦'}

// starInk is the color of a particle at this brightness: the muted ink
// for the faint ones, climbing the working glow to the crest for a flare.
func starInk(level starLevel) color.Color {
	switch level {
	case starFlare:
		return theme.Color(stormlightGlow[3])
	case starBright:
		return theme.Color(stormlightGlow[2])
	case starSoft:
		return theme.Color(stormlightGlow[0])
	}
	return colorMuted()
}

// The particle field. Each particle has a fixed place, a life of
// particleLifeMin to particleLifeMax ticks that it repeats forever, and
// an offset into it, so the field glimmers out of step. Over a life it
// rises from dark to its peak and sinks back; most peak faint, a few
// flare.
const (
	particleDensity = 45 // particles per thousand cells
	particleLifeMin = 24
	particleLifeMax = 48
)

// particleAt is the particle in this cell at this beat, if there is one
// and it is showing.
func particleAt(col, row, beat int) (starLevel, bool) {
	hash := skyHash(col, row, 7)
	if hash%1000 >= particleDensity {
		return starDark, false
	}
	life := particleLifeMin + (hash/1000)%(particleLifeMax-particleLifeMin)
	offset := (hash / 100000) % life
	tick := (beat + offset) % life
	// A triangle over the life: dark at both ends, full in the middle.
	height := 1 - math.Abs(2*float64(tick)/float64(life)-1)
	bright := (hash/10000)%10 >= 7
	var level starLevel
	switch {
	case height < 0.25:
		return starDark, false
	case height < 0.5:
		level = starFaint
	case height < 0.8 || !bright:
		level = starFaint
		if bright {
			level = starSoft
		}
	case height < 0.95:
		level = starBright
	default:
		level = starFlare
	}
	return level, true
}

// A meteor crosses the sky every meteorEvery beats: a flare at the head
// and a trail of fading points behind it, moving two columns and one row
// a beat for meteorLife beats. Where it starts is hashed from which
// meteor it is, so no two take the same path and every path repeats.
const (
	meteorEvery = 120
	meteorLife  = 8
	meteorTrail = 4
)

var meteorTrailLevels = [meteorTrail]starLevel{starBright, starSoft, starFaint, starFaint}

// paintMeteor draws the current meteor, if one is in flight.
func paintMeteor(c *canvas, beat, skyHeight int) {
	n, t := beat/meteorEvery, beat%meteorEvery
	if t >= meteorLife {
		return
	}
	originCol := skyHash(n, 1, 1) % max(1, c.width-2*meteorLife)
	originRow := skyHash(n, 2, 2) % max(1, skyHeight-meteorLife)
	c.set(originRow+t, originCol+2*t, '✦', starInk(starFlare))
	for k := 1; k <= meteorTrail; k++ {
		back := t - k
		if back < 0 {
			break
		}
		level := meteorTrailLevels[k-1]
		c.set(originRow+back, originCol+2*back, starGlyphs[level], starInk(level))
	}
}

// The sun: a disc of block shade in gold, with a corona of flecks that
// flicker around it.
var (
	sunDisc = [3]string{" ▒▓▒ ", "▒▓█▓▒", " ▒▓▒ "}
	sunInk  = map[rune]theme.Pair{
		'█': {Light: "#B8860B", Dark: "#FFF1C1"},
		'▓': {Light: "#C98A1A", Dark: "#F7D774"},
		'▒': {Light: "#D9A441", Dark: "#E0B04A"},
		'░': {Light: "#E2BD6E", Dark: "#A87F2E"},
	}
	// Corona flecks, as column and row offsets from the sun's heart.
	sunCorona = [8][2]int{{-4, 0}, {4, 0}, {0, -2}, {0, 2}, {-3, -1}, {3, 1}, {3, -1}, {-3, 1}}
)

func paintSun(c *canvas, heartRow, heartCol, beat int) {
	for k, fleck := range sunCorona {
		if (beat/3+k)%3 == 0 {
			c.set(heartRow+fleck[1], heartCol+fleck[0], '░', theme.Color(sunInk['░']))
		}
	}
	for row, line := range sunDisc {
		for col, r := range []rune(line) {
			if r != ' ' {
				c.set(heartRow-1+row, heartCol-2+col, r, theme.Color(sunInk[r]))
			}
		}
	}
}

// The planets of the Rosharan system, inner to outer. orbit is the
// orbit's radius as a fraction of the biggest the pane allows; period
// is beats per revolution, Kepler's way — the inner world hurries, the
// outer one crawls; start is where in the orbit each begins, so they
// never line up.
type planet struct {
	name   string
	orbit  float64
	period int
	start  float64
	glyph  rune
	ink    theme.Pair
	moon   bool
}

var planets = [3]planet{
	{name: "Ashyn", orbit: 0.38, period: 36, start: 0.3, glyph: '•',
		ink: theme.Pair{Light: "#C2552B", Dark: "#F08A5D"}},
	{name: "Roshar", orbit: 0.68, period: 80, start: 2.1, glyph: '●',
		ink: stormlightGlow[1], moon: true},
	{name: "Braize", orbit: 1.0, period: 160, start: 4.0, glyph: '◉',
		ink: theme.Pair{Light: "#5B4A8A", Dark: "#A596DC"}},
}

// Trails: a planet leaves trailLength points behind it, one every
// trailSpacing beats, so the eye sees the motion even between ticks.
const (
	trailLength  = 2
	trailSpacing = 2
	moonPeriod   = 10
	orbitDotArc  = 3.5 // cells of arc per orbit dot
)

// orbitPoint is where a body sits on an orbit of this radius (in rows) at
// this angle: columns run twice as fast as rows so the orbit is round.
func orbitPoint(heartRow, heartCol int, radius, angle float64) (row, col int) {
	return heartRow + int(math.Round(radius*math.Sin(angle))),
		heartCol + int(math.Round(2*radius*math.Cos(angle)))
}

// paintOrbits lays each orbit down as sparse dots in the dimmest ink,
// over the particles and under everything else.
func paintOrbits(c *canvas, heartRow, heartCol int, reach float64) {
	ink := colorBorder()
	for _, p := range planets {
		radius := p.orbit * reach
		// The ellipse's mean axis is 1.5 radii; one dot per orbitDotArc
		// cells of it.
		dots := max(8, int(2*math.Pi*1.5*radius/orbitDotArc))
		for i := 0; i < dots; i++ {
			row, col := orbitPoint(heartRow, heartCol, radius, 2*math.Pi*float64(i)/float64(dots))
			c.set(row, col, '·', ink)
		}
	}
}

// paintPlanets draws each planet at this beat with its trail, and
// Roshar's moon circling it.
func paintPlanets(c *canvas, heartRow, heartCol int, reach float64, beat int) {
	for _, p := range planets {
		radius := p.orbit * reach
		angleAt := func(b int) float64 {
			return p.start + 2*math.Pi*float64(b)/float64(p.period)
		}
		for k := trailLength; k >= 1; k-- {
			row, col := orbitPoint(heartRow, heartCol, radius, angleAt(beat-k*trailSpacing))
			c.set(row, col, '∙', colorMuted())
		}
		row, col := orbitPoint(heartRow, heartCol, radius, angleAt(beat))
		c.set(row, col, p.glyph, theme.Color(p.ink))
		if p.moon {
			moonAngle := 2 * math.Pi * float64(beat) / moonPeriod
			moonRow, moonCol := orbitPoint(row, col, 1, moonAngle)
			c.set(moonRow, moonCol, '·', colorMuted())
		}
	}
}

// cell is one painted terminal cell: a rune and the color it wears. A nil
// color is the pane's own text color, which the sky never uses.
type cell struct {
	r     rune
	color color.Color
}

// canvas is a grid of cells painted into before being rendered row by
// row, because particles, orbits, planets, a meteor, and a caption
// overlap in ways that strings do not compose.
type canvas struct {
	width, height int
	cells         [][]cell
}

func newCanvas(width, height int) *canvas {
	cells := make([][]cell, height)
	for row := range cells {
		cells[row] = make([]cell, width)
		for col := range cells[row] {
			cells[row][col] = cell{r: ' '}
		}
	}
	return &canvas{width: width, height: height, cells: cells}
}

func (c *canvas) set(row, col int, r rune, color color.Color) {
	if row < 0 || row >= c.height || col < 0 || col >= c.width {
		return
	}
	c.cells[row][col] = cell{r: r, color: color}
}

// fill paints a string with every cell opaque, spaces included, so the
// caption clears the sky around itself.
func (c *canvas) fill(row, col int, text string, color color.Color) {
	for offset, r := range []rune(text) {
		c.set(row, col+offset, r, color)
	}
}

// render emits the grid, one styled run per stretch of identical color so
// the output stays small enough to diff by eye in a test.
func (c *canvas) render() string {
	rows := make([]string, c.height)
	for row, cells := range c.cells {
		var out strings.Builder
		var run strings.Builder
		var runColor color.Color
		flush := func() {
			if run.Len() == 0 {
				return
			}
			if runColor == nil {
				out.WriteString(run.String())
			} else {
				out.WriteString(lipgloss.NewStyle().Foreground(runColor).Render(run.String()))
			}
			run.Reset()
		}
		for _, cell := range cells {
			if cell.color != runColor {
				flush()
				runColor = cell.color
			}
			run.WriteRune(cell.r)
		}
		flush()
		rows[row] = out.String()
	}
	return strings.Join(rows, "\n")
}

// The system needs an outer orbit of at least skyMinReach rows, which
// with the caption's rows and a margin sets the smallest pane that gets
// a sky. Below that the pane shows the caption alone, centered, which is
// what these states always were.
const (
	skyMinReach  = 4
	skyMinHeight = 2*skyMinReach + 2 + 2
	skyMinWidth  = 4*skyMinReach + 6
)

// Pace is how fast the sky moves: a pane at rest turns slowly, a pane
// with a terminal starting behind it moves with purpose.
const (
	paceIdle     = 1
	paceStarting = 3
)

// renderSky is the Rosharan system filling a pane of this size at this
// phase and pace, with a caption beneath it. A negative phase is the
// tick at rest: the sky holds its first frame.
func renderSky(width, height int, caption string, phase, pace int) string {
	if width < skyMinWidth || height < skyMinHeight {
		return lipgloss.Place(width, max(1, height), lipgloss.Center, lipgloss.Center,
			mutedStyle().Render(truncate(caption, width)))
	}
	beat := max(0, phase) * pace
	c := newCanvas(width, height)
	skyHeight := height - 2
	for row := 0; row < skyHeight; row++ {
		for col := 0; col < width; col++ {
			if level, ok := particleAt(col, row, beat); ok {
				c.set(row, col, starGlyphs[level], starInk(level))
			}
		}
	}
	heartRow, heartCol := skyHeight/2, width/2
	reach := float64(min((skyHeight-2)/2, (width-6)/4))
	paintOrbits(c, heartRow, heartCol, reach)
	paintMeteor(c, beat, skyHeight)
	paintSun(c, heartRow, heartCol, beat)
	paintPlanets(c, heartRow, heartCol, reach, beat)
	caption = truncate(caption, width)
	captionWidth := lipgloss.Width(caption)
	c.fill(height-2, (width-captionWidth)/2-1, " "+caption+" ", colorMuted())
	return c.render()
}

// startingCaption names what the pane is waiting on. A provider's name is
// the one fact that makes "starting" worth reading, so it goes in when the
// agent has one.
func startingCaption(provider string) string {
	if provider == "" {
		return "Starting terminal…"
	}
	return fmt.Sprintf("Starting %s…", provider)
}
