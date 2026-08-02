package ui

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// pathNav is the fzf-shaped picker behind "Enter a path": every directory
// under the root (a few levels deep) in one flat list, narrowed live as you
// type, Enter picks the highlighted one. Backspace on an empty filter
// re-roots one level up; a typed absolute or ~ path re-roots there.
type pathNav struct {
	root      string
	filter    lineInput
	entries   []string
	highlight int
	loadErr   error
}

// rootEntry represents the root itself at the top of the list, so pressing
// Enter immediately chooses the directory the picker opened in.
const rootEntry = "."

const (
	pathNavDepth      = 4
	pathNavMaxEntries = 3000
)

// pathNavSkip are directory names that would flood the list with noise.
var pathNavSkip = map[string]bool{"node_modules": true}

func newPathNav(start string) pathNav {
	if !isDirectory(start) {
		start = string(filepath.Separator)
	}
	nav := pathNav{
		root:   filepath.Clean(start),
		filter: newLineInput("type to filter"),
	}
	nav.filter.Focus()
	nav.reload()
	return nav
}

func (n *pathNav) reload() {
	n.entries = nil
	n.loadErr = nil
	n.highlight = 0
	if _, err := os.ReadDir(n.root); err != nil {
		n.loadErr = err
		return
	}
	n.walk(n.root, "", 1)
	sort.Slice(n.entries, func(a, b int) bool {
		depthA := strings.Count(n.entries[a], string(filepath.Separator))
		depthB := strings.Count(n.entries[b], string(filepath.Separator))
		if depthA != depthB {
			return depthA < depthB
		}
		return n.entries[a] < n.entries[b]
	})
}

func (n *pathNav) walk(dir, prefix string, depth int) {
	if depth > pathNavDepth || len(n.entries) >= pathNavMaxEntries {
		return
	}
	items, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, item := range items {
		if len(n.entries) >= pathNavMaxEntries {
			return
		}
		name := item.Name()
		if strings.HasPrefix(name, ".") || pathNavSkip[name] {
			continue
		}
		if !item.IsDir() {
			if item.Type()&os.ModeSymlink == 0 {
				continue
			}
			if info, err := os.Stat(filepath.Join(dir, name)); err != nil || !info.IsDir() {
				continue
			}
			// Symlinked directories are listed but not walked, so cycles
			// cannot recurse.
			n.entries = append(n.entries, filepath.Join(prefix, name))
			continue
		}
		relative := filepath.Join(prefix, name)
		n.entries = append(n.entries, relative)
		n.walk(filepath.Join(dir, name), relative, depth+1)
	}
}

// matches narrows entries fzf-style: every whitespace-separated term must
// appear in the relative path, case-insensitively. The root itself leads
// the unfiltered list.
func (n pathNav) matches() []string {
	terms := strings.Fields(strings.ToLower(n.filter.Value()))
	if len(terms) == 0 {
		return append([]string{rootEntry}, n.entries...)
	}
	var results []string
	for _, entry := range n.entries {
		lowered := strings.ToLower(entry)
		hit := true
		for _, term := range terms {
			if !strings.Contains(lowered, term) {
				hit = false
				break
			}
		}
		if hit {
			results = append(results, entry)
		}
	}
	return results
}

func (n *pathNav) moveHighlight(delta int) {
	count := len(n.matches())
	if count == 0 {
		n.highlight = 0
		return
	}
	n.highlight = (n.highlight + delta + count) % count
}

// chosen resolves the highlighted match to an absolute path, or "" when
// nothing matches.
func (n pathNav) chosen() string {
	matches := n.matches()
	if len(matches) == 0 {
		return ""
	}
	pick := matches[min(n.highlight, len(matches)-1)]
	if pick == rootEntry {
		return n.root
	}
	return filepath.Join(n.root, pick)
}

// reroot moves the picker's root, clearing the filter.
func (n *pathNav) reroot(dir string) {
	n.root = filepath.Clean(dir)
	n.filter.SetValue("")
	n.reload()
}

func (n *pathNav) up() {
	n.reroot(filepath.Dir(n.root))
}

// jump re-roots at a typed absolute or ~ path, reporting whether the filter
// looked like one and whether it resolved.
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
	n.reroot(expanded)
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

// update routes a key to the filter and re-anchors the highlight on the
// best match whenever the text changes.
func (n *pathNav) update(msg tea.KeyMsg) {
	before := n.filter.Value()
	n.filter = n.filter.Update(msg)
	if n.filter.Value() != before {
		n.highlight = 0
	}
}

func (n pathNav) filterEmpty() bool {
	return strings.TrimSpace(n.filter.Value()) == ""
}

// view renders the root, the filter line, and the match list with the
// highlight kept in view.
func (n pathNav) view(width, maxRows int) string {
	width = max(10, width)
	n.filter.SetWidth(max(1, width-2))
	lines := []string{
		accentStyle.Render("cd ") +
			mutedStyle.Render(truncatePathTail(shortPath(n.root), max(1, width-3))),
		"> " + n.filter.View(),
	}
	if n.loadErr != nil {
		lines = append(lines, errorStyle.Render(truncate("cannot read directory", width)))
		return strings.Join(lines, "\n")
	}
	matches := n.matches()
	if len(matches) == 0 {
		lines = append(lines, mutedStyle.Render(truncate("no matching directories", width)))
		return strings.Join(lines, "\n")
	}
	highlight := min(n.highlight, len(matches)-1)
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
		if name == rootEntry {
			style = mutedStyle
		}
		lines = append(lines, " "+style.Render(truncate(name, max(1, width-1))))
	}
	if remaining := len(matches) - start - rows; remaining > 0 {
		lines = append(lines, mutedStyle.Render(truncate(
			"… "+itoa(remaining)+" more", width)))
	}
	return strings.Join(lines, "\n")
}

func itoa(value int) string {
	if value == 0 {
		return "0"
	}
	digits := ""
	for value > 0 {
		digits = string(rune('0'+value%10)) + digits
		value /= 10
	}
	return digits
}
