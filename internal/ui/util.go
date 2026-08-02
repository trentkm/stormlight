package ui

// Small shared text and path helpers.
// Split from model.go; see #34.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"
)

func shortPath(path string) string {
	home, err := os.UserHomeDir()
	if err == nil && strings.HasPrefix(path, home) {
		path = "~" + strings.TrimPrefix(path, home)
	}
	return filepath.Clean(path)
}

func truncatePathTail(path string, width int) string {
	if width <= 0 || strings.TrimSpace(path) == "" {
		return ""
	}
	path = shortPath(path)
	pathWidth := ansi.StringWidth(path)
	if pathWidth <= width {
		return path
	}
	prefix := "…"
	removeWidth := pathWidth - max(1, width-ansi.StringWidth(prefix))
	return ansi.TruncateLeft(path, removeWidth, prefix)
}

func timeAgo(created time.Time) string {
	if created.IsZero() {
		return ""
	}
	elapsed := time.Since(created)
	switch {
	case elapsed < time.Minute:
		return "now"
	case elapsed < time.Hour:
		return fmt.Sprintf("%dm", int(elapsed.Minutes()))
	case elapsed < 24*time.Hour:
		return fmt.Sprintf("%dh", int(elapsed.Hours()))
	default:
		return fmt.Sprintf("%dd", int(elapsed.Hours()/24))
	}
}

func truncate(value string, width int) string {
	if width <= 0 {
		return ""
	}
	return ansi.Truncate(strings.Join(strings.Fields(value), " "), width, "…")
}

func visibleRange(total, cursor, limit int) (int, int) {
	if total <= 0 || limit <= 0 {
		return 0, 0
	}
	limit = min(limit, total)
	cursor = clamp(cursor, 0, total-1)
	start := clamp(cursor-limit/2, 0, total-limit)
	return start, start + limit
}

func clamp(value, low, high int) int {
	if value < low {
		return low
	}
	if value > high {
		return high
	}
	return value
}
