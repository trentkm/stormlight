package ui

import (
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// pathNav is the interactive cd behind "Enter a path": a filterable list of
// the current directory's subdirectories with zsh-completion ergonomics —
// type to filter, Tab (or Enter on a match) descends, Backspace on an empty
// filter (or the .. entry) goes up, Enter with nothing highlighted confirms
// the directory it is sitting in.
type pathNav struct {
	base    string
	filter  lineInput
	entries []string
	loadErr error
	// cycle is the completion snapshot while Tab is walking candidates:
	// the list is frozen at cycle start so filling a candidate's name into
	// the filter doesn't narrow the very list being walked.
	cycle      []string
	cycleIndex int
}

const parentEntry = ".."

func newPathNav(start string) pathNav {
	if !isDirectory(start) {
		start = string(filepath.Separator)
	}
	nav := pathNav{
		base:   filepath.Clean(start),
		filter: newLineInput("type to filter"),
	}
	nav.filter.Focus()
	nav.reload()
	return nav
}

func (n *pathNav) reload() {
	n.entries = nil
	n.loadErr = nil
	items, err := os.ReadDir(n.base)
	if err != nil {
		n.loadErr = err
		return
	}
	for _, item := range items {
		if !item.IsDir() {
			if item.Type()&os.ModeSymlink == 0 {
				continue
			}
			if info, err := os.Stat(filepath.Join(n.base, item.Name())); err != nil || !info.IsDir() {
				continue
			}
		}
		n.entries = append(n.entries, item.Name())
	}
	sort.Strings(n.entries)
}

// matches filters entries by the typed text: prefix matches first, then
// substring matches, case-insensitively. Hidden directories stay out of the
// way unless the filter itself starts with a dot. An empty filter leads
// with the parent entry so .. is always one keystroke away.
func (n pathNav) matches() []string {
	needle := strings.ToLower(strings.TrimSpace(n.filter.Value()))
	var prefixed, contained []string
	for _, entry := range n.entries {
		lowered := strings.ToLower(entry)
		if strings.HasPrefix(entry, ".") && !strings.HasPrefix(needle, ".") {
			continue
		}
		switch {
		case needle == "" || strings.HasPrefix(lowered, needle):
			prefixed = append(prefixed, entry)
		case strings.Contains(lowered, needle):
			contained = append(contained, entry)
		}
	}
	results := append(prefixed, contained...)
	if needle == "" && n.base != string(filepath.Separator) {
		results = append([]string{parentEntry}, results...)
	}
	return results
}

// complete is the fish Tab: the first press extends the filter to the
// candidates' longest common prefix; once no prefix progress is possible,
// repeated presses cycle the candidates, filling each name into the filter.
func (n *pathNav) complete(delta int) {
	if n.cycle == nil {
		candidates := n.matches()
		if len(candidates) == 0 {
			return
		}
		prefix := commonPrefix(candidates)
		if len(prefix) > len(strings.TrimSpace(n.filter.Value())) {
			n.filter.SetValue(prefix)
			if len(candidates) == 1 {
				return
			}
			// Prefix landed exactly on a full candidate: fall through to
			// cycling so the next press moves on.
			if !strings.EqualFold(prefix, candidates[0]) {
				return
			}
		}
		n.cycle = candidates
		if delta >= 0 {
			n.cycleIndex = 0
		} else {
			n.cycleIndex = len(candidates) - 1
		}
		n.filter.SetValue(n.cycle[n.cycleIndex])
		return
	}
	n.cycleIndex = (n.cycleIndex + delta + len(n.cycle)) % len(n.cycle)
	n.filter.SetValue(n.cycle[n.cycleIndex])
}

// commonPrefix is the longest shared prefix of the candidates, compared
// case-insensitively and spelled with the first candidate's casing. The
// parent entry never participates.
func commonPrefix(candidates []string) string {
	prefix := ""
	started := false
	for _, candidate := range candidates {
		if candidate == parentEntry {
			continue
		}
		if !started {
			prefix = candidate
			started = true
			continue
		}
		limit := min(len(prefix), len(candidate))
		matched := 0
		for matched < limit &&
			strings.EqualFold(prefix[matched:matched+1], candidate[matched:matched+1]) {
			matched++
		}
		prefix = prefix[:matched]
	}
	return prefix
}

type pathNavAction int

const (
	pathNavHandled pathNavAction = iota
	pathNavConfirm
	pathNavInvalid
)

// enter resolves the current line the way cd would: an empty line confirms
// the directory the navigator is in; an absolute or ~ path jumps; anything
// else descends into the current candidate (the cycled one, or the best
// match for what was typed).
func (n *pathNav) enter() pathNavAction {
	if n.cycle != nil {
		choice := n.cycle[n.cycleIndex]
		n.cycle = nil
		if choice == parentEntry {
			n.up()
			return pathNavHandled
		}
		n.setBase(filepath.Join(n.base, choice))
		return pathNavHandled
	}
	if n.filterEmpty() {
		return pathNavConfirm
	}
	if attempted, ok := n.jump(); attempted {
		if !ok {
			return pathNavInvalid
		}
		return pathNavHandled
	}
	matches := n.matches()
	if len(matches) == 0 {
		return pathNavInvalid
	}
	if matches[0] == parentEntry {
		n.up()
		return pathNavHandled
	}
	n.setBase(filepath.Join(n.base, matches[0]))
	return pathNavHandled
}

func (n *pathNav) up() {
	n.setBase(filepath.Dir(n.base))
}

func (n *pathNav) setBase(dir string) {
	n.base = filepath.Clean(dir)
	n.filter.SetValue("")
	n.cycle = nil
	n.reload()
}

// jump resolves a typed absolute or ~ path and enters it, reporting whether
// the filter looked like such a path and whether it resolved.
func (n *pathNav) jump() (attempted, ok bool) {
	typed := strings.TrimSpace(n.filter.Value())
	if !strings.HasPrefix(typed, string(filepath.Separator)) &&
		!strings.HasPrefix(typed, "~") {
		return false, false
	}
	expanded := expandHomePath(typed)
	if !isDirectory(expanded) {
		return true, false
	}
	n.setBase(expanded)
	return true, true
}

func expandHomePath(path string) string {
	if path == "~" || strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, strings.TrimPrefix(path, "~"))
		}
	}
	return path
}

// update routes a key through the filter input and resets the highlight
// whenever the text changes.
func (n *pathNav) update(msg tea.KeyMsg) {
	before := n.filter.Value()
	n.filter = n.filter.Update(msg)
	if n.filter.Value() != before {
		n.cycle = nil
	}
}

func (n pathNav) filterEmpty() bool {
	return strings.TrimSpace(n.filter.Value()) == ""
}

// view renders the cd header, the filter line, and up to maxRows matches.
func (n pathNav) view(width, maxRows int) string {
	width = max(10, width)
	n.filter.SetWidth(max(1, width-2))
	lines := []string{
		accentStyle.Render("cd ") +
			mutedStyle.Render(truncatePathTail(shortPath(n.base), max(1, width-3))),
		"> " + n.filter.View(),
	}
	if n.loadErr != nil {
		lines = append(lines, errorStyle.Render(truncate("cannot read directory", width)))
		return strings.Join(lines, "\n")
	}
	matches := n.matches()
	highlight := -1
	if n.cycle != nil {
		matches = n.cycle
		highlight = n.cycleIndex
	}
	if len(matches) == 0 {
		lines = append(lines, mutedStyle.Render(truncate("no matching directories", width)))
		return strings.Join(lines, "\n")
	}
	rows := min(len(matches), max(1, maxRows))
	start := 0
	if highlight >= rows {
		start = highlight - rows + 1
	}
	for index := start; index < start+rows && index < len(matches); index++ {
		name := matches[index]
		if index == highlight {
			lines = append(lines, selectTheme.selectableRow(name, width, true))
			continue
		}
		style := titleStyle
		if name == parentEntry {
			style = mutedStyle
		}
		lines = append(lines, " "+style.Render(truncate(name, max(1, width-1))))
	}
	if len(matches) > start+rows {
		lines = append(lines, mutedStyle.Render(truncate(
			"… "+strconv.Itoa(len(matches)-start-rows)+" more", width)))
	}
	return strings.Join(lines, "\n")
}
