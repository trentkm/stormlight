package ui

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/trentkm/stormlight/internal/agent"
	"github.com/trentkm/stormlight/internal/ptyview"
)

// skyRows is a rendered sky as plain text, one string per row.
func skyRows(sky string) []string {
	return strings.Split(ansi.Strip(sky), "\n")
}

// skyCells is a rendered sky as a grid of runes.
func skyCells(sky string) [][]rune {
	rows := skyRows(sky)
	cells := make([][]rune, len(rows))
	for row, line := range rows {
		cells[row] = []rune(line)
	}
	return cells
}

// find is the first cell holding this rune, or (-1, -1).
func find(cells [][]rune, want rune) (row, col int) {
	for row, line := range cells {
		for col, r := range line {
			if r == want {
				return row, col
			}
		}
	}
	return -1, -1
}

const invitation = "No agents yet — press n to dispatch one"

func TestSkyFillsThePaneExactly(t *testing.T) {
	for _, size := range [][2]int{{22, 12}, {44, 12}, {100, 30}, {61, 13}} {
		width, height := size[0], size[1]
		for _, phase := range []int{-1, 0, 1, 7, 50, 500} {
			rows := skyRows(renderSky(width, height, invitation, phase, paceIdle))
			if len(rows) != height {
				t.Fatalf("%dx%d phase %d: %d rows", width, height, phase, len(rows))
			}
			for index, row := range rows {
				if got := lipgloss.Width(row); got != width {
					t.Fatalf("%dx%d phase %d row %d is %d wide: %q", width, height, phase, index, got, row)
				}
			}
		}
	}
}

func TestSkyFallsBackToTheCaptionWhenCramped(t *testing.T) {
	for _, size := range [][2]int{{21, 20}, {40, 11}, {8, 3}} {
		sky := ansi.Strip(renderSky(size[0], size[1], invitation, 10, paceIdle))
		if strings.ContainsAny(sky, "·•✦░▒▓█●◉") {
			t.Fatalf("%dx%d drew the sky into a pane too small for it:\n%s", size[0], size[1], sky)
		}
		// Narrower than the caption, it is cut the way it always was.
		if !strings.Contains(sky, "No agen") {
			t.Fatalf("%dx%d lost the caption:\n%s", size[0], size[1], sky)
		}
	}
}

func TestParticlesGlimmerInAndOut(t *testing.T) {
	first := ansi.Strip(renderSky(80, 24, invitation, 0, paceIdle))
	stars := strings.Count(first, "·") + strings.Count(first, "•") + strings.Count(first, "✦")
	if stars < 30 {
		t.Fatalf("a sky with %d points of light is not a sky:\n%s", stars, first)
	}
	later := ansi.Strip(renderSky(80, 24, invitation, 3, paceIdle))
	if first == later {
		t.Fatal("three ticks on, nothing in the sky had changed")
	}
	// A particle, watched over its life, shows and goes dark again. Not
	// every cell holds one, so the first that does is the one watched.
	var seenDark, seenLit bool
	for col := 0; col < 200 && !(seenDark && seenLit); col++ {
		if _, holds := particleAt(col, 3, 0); !holds {
			if _, holds := particleAt(col, 3, particleLifeMax/2); !holds {
				continue
			}
		}
		seenDark, seenLit = false, false
		for beat := 0; beat < particleLifeMax; beat++ {
			_, lit := particleAt(col, 3, beat)
			seenDark = seenDark || !lit
			seenLit = seenLit || lit
		}
	}
	if !seenDark || !seenLit {
		t.Fatal("no particle both showed and went dark within one life")
	}
}

func TestSkyIsAPureFunctionOfItsPhase(t *testing.T) {
	for _, phase := range []int{0, 7, 13, 40} {
		a := renderSky(80, 20, "Starting claude…", phase, paceStarting)
		b := renderSky(80, 20, "Starting claude…", phase, paceStarting)
		if a != b {
			t.Fatalf("phase %d rendered two different frames", phase)
		}
	}
}

func TestRestingPhaseHoldsTheFirstFrame(t *testing.T) {
	if renderSky(60, 12, invitation, -1, paceIdle) != renderSky(60, 12, invitation, 0, paceIdle) {
		t.Fatal("the tick at rest is not the sky's first frame")
	}
}

func TestTheSunSitsAtTheHeart(t *testing.T) {
	cells := skyCells(renderSky(60, 14, invitation, 9, paceIdle))
	if string(cells[6][28:33]) != "▒▓█▓▒" {
		t.Fatalf("no sun at the center of the sky:\n%s", ansi.Strip(renderSky(60, 14, invitation, 9, paceIdle)))
	}
}

func TestThePlanetsOrbit(t *testing.T) {
	const width, height = 80, 24
	for _, p := range planets {
		if p.glyph == '•' {
			continue // Ashyn shares a glyph with a soft particle; it is told apart by color.
		}
		first := skyCells(renderSky(width, height, invitation, 0, paceIdle))
		row0, col0 := find(first, p.glyph)
		if row0 < 0 {
			t.Fatalf("%s missing at phase 0", p.name)
		}
		later := skyCells(renderSky(width, height, invitation, p.period/4, paceIdle))
		row1, col1 := find(later, p.glyph)
		if row1 < 0 {
			t.Fatalf("%s missing a quarter orbit on", p.name)
		}
		if row0 == row1 && col0 == col1 {
			t.Fatalf("%s did not move in a quarter of its orbit", p.name)
		}
		// A full orbit later it is back where it began.
		again := skyCells(renderSky(width, height, invitation, p.period, paceIdle))
		row2, col2 := find(again, p.glyph)
		if row2 != row0 || col2 != col0 {
			t.Fatalf("%s did not return after one orbit: (%d,%d) then (%d,%d)", p.name, row0, col0, row2, col2)
		}
	}
}

func TestAStartingPaneMovesFaster(t *testing.T) {
	idle := skyCells(renderSky(80, 24, invitation, 10, paceIdle))
	fast := skyCells(renderSky(80, 24, invitation, 10, paceStarting))
	far := skyCells(renderSky(80, 24, invitation, 30, paceIdle))
	r0, c0 := find(idle, '◉')
	r1, c1 := find(fast, '◉')
	r2, c2 := find(far, '◉')
	if r0 == r1 && c0 == c1 {
		t.Fatal("pace made no difference to Braize")
	}
	if r1 != r2 || c1 != c2 {
		t.Fatal("pace is not simply more beats per tick")
	}
}

func TestAMeteorCrossesEveryCycle(t *testing.T) {
	// Mid-flight the head leads a fading trail up the diagonal: each
	// point one row up and two columns left of the one before it.
	cells := skyCells(renderSky(80, 24, invitation, 4, paceIdle))
	var found bool
	for row := 4; row < len(cells) && !found; row++ {
		for col := 8; col < len(cells[row]); col++ {
			if cells[row][col] == '✦' && cells[row-1][col-2] == '✦' &&
				cells[row-2][col-4] == '•' && cells[row-3][col-6] == '·' {
				found = true
				break
			}
		}
	}
	if !found {
		t.Fatalf("no meteor with a trail at phase 4:\n%s", ansi.Strip(renderSky(80, 24, invitation, 4, paceIdle)))
	}
}

func TestTheCaptionIsClear(t *testing.T) {
	for _, phase := range []int{0, 9, 77} {
		rows := skyRows(renderSky(60, 12, invitation, phase, paceIdle))
		if !strings.Contains(rows[10], " "+invitation+" ") {
			t.Fatalf("phase %d: something landed on the caption:\n%s", phase, strings.Join(rows, "\n"))
		}
	}
}

func TestStartingCaptionNamesTheProvider(t *testing.T) {
	if got := startingCaption("codex"); got != "Starting codex…" {
		t.Fatalf("caption = %q", got)
	}
	if got := startingCaption(""); got != "Starting terminal…" {
		t.Fatalf("caption without provider = %q", got)
	}
}

// skyFixture is a dashboard in terminal mode whose selected agent has no
// terminal open yet.
func skyFixture() Model {
	codex := agent.Agent{ID: "a1", Provider: agent.ProviderCodex}
	return Model{
		mode:          modeNormal,
		activePane:    paneInteraction,
		ptyEnabled:    true,
		ptyManager:    ptyview.NewManager(nil),
		width:         120,
		height:        40,
		agents:        []agent.Agent{codex},
		groups:        []workspaceGroup{{agents: []agent.Agent{codex}}},
		keys:          defaultKeyBindings(),
		ready:         true,
		loaded:        true,
		catalogLoaded: true,
	}
}

func TestStartingTerminalDrawsTheSky(t *testing.T) {
	model := skyFixture()
	model.shimmerRunning = true
	model.shimmerPhase = 9
	managedAgent, _ := model.selectedAgent()
	pane := ansi.Strip(model.renderPTYInteraction(managedAgent, 0, 0))
	if !strings.Contains(pane, "Starting codex…") {
		t.Fatalf("starting pane does not name what it waits on:\n%s", pane)
	}
	if !strings.Contains(pane, "▒▓█▓▒") {
		t.Fatalf("starting pane has no sun:\n%s", pane)
	}
}

func TestEmptyPortalDrawsTheSky(t *testing.T) {
	model := skyFixture()
	model.groups = nil
	model.shimmerRunning = true
	pane := ansi.Strip(model.renderEmptyPortal(80, 24))
	if !strings.Contains(pane, "Add a workspace to begin") || !strings.Contains(pane, "▒▓█▓▒") {
		t.Fatalf("empty portal is not a sky over the invitation:\n%s", pane)
	}
}

func TestTheSkyKeepsTheTickAlive(t *testing.T) {
	model := skyFixture()
	if !model.skyAnimating() {
		t.Fatal("a selected agent with no terminal is not drawing the sky")
	}
	model.shimmerRunning = true
	updated, cmd := model.Update(shimmerTickMsg{})
	model = updated.(Model)
	if !model.shimmerRunning || cmd == nil {
		t.Fatal("the tick stopped while a terminal was still opening")
	}

	// Nothing selected is the sky too.
	model.groups = nil
	if !model.skyAnimating() {
		t.Fatal("an empty pane is not drawing the sky")
	}
	updated, _ = model.Update(shimmerTickMsg{})
	if !updated.(Model).shimmerRunning {
		t.Fatal("the tick stopped over an empty pane")
	}

	// The transcript view with an agent selected is neither.
	model = skyFixture()
	model.ptyEnabled = false
	model.shimmerRunning = true
	updated, _ = model.Update(shimmerTickMsg{})
	if updated.(Model).shimmerRunning {
		t.Fatal("the tick ran on with nothing left to animate")
	}
}

func TestEnteringTerminalModeStartsTheTick(t *testing.T) {
	model := skyFixture()
	model.ptyEnabled = false
	cmd := model.togglePTY()
	if !model.shimmerRunning || cmd == nil {
		t.Fatal("entering a starting terminal did not start the tick")
	}
}
