package diagnostic

import (
	"bufio"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
)

const maxLogSize = 5 << 20

var state = struct {
	sync.RWMutex
	logger *slog.Logger
	file   *os.File
	path   string
}{
	logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
}

func Init(requestedPath, levelName string) (string, error) {
	path, err := ResolvePath(requestedPath)
	if err != nil {
		return "", err
	}
	level, err := parseLevel(levelName)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", fmt.Errorf("create log directory: %w", err)
	}
	if err := rotate(path); err != nil {
		return "", err
	}
	file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return "", fmt.Errorf("open log file: %w", err)
	}
	logger := slog.New(slog.NewJSONHandler(file, &slog.HandlerOptions{
		AddSource: level == slog.LevelDebug,
		Level:     level,
	}))

	state.Lock()
	previous := state.file
	state.file = file
	state.logger = logger
	state.path = path
	state.Unlock()
	if previous != nil {
		_ = previous.Close()
	}

	logger.Info("logging initialized",
		"path", path,
		"configured_level", level.String(),
		"pid", os.Getpid(),
	)
	return path, nil
}

func Logger() *slog.Logger {
	state.RLock()
	defer state.RUnlock()
	return state.logger
}

func Path() string {
	state.RLock()
	defer state.RUnlock()
	return state.path
}

func Close() {
	state.Lock()
	defer state.Unlock()
	if state.file != nil {
		_ = state.file.Close()
		state.file = nil
	}
}

func Tail(path string, lines int) (string, error) {
	if lines <= 0 {
		lines = 100
	}
	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open log file: %w", err)
	}
	defer file.Close()

	all := make([]string, 0, lines)
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		all = append(all, scanner.Text())
		if len(all) > lines {
			copy(all, all[len(all)-lines:])
			all = all[:lines]
		}
	}
	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("read log file: %w", err)
	}
	return strings.Join(all, "\n"), nil
}

func ResolvePath(requested string) (string, error) {
	if requested == "" {
		requested = os.Getenv("STORMLIGHT_LOG_FILE")
	}
	if requested != "" {
		path, err := filepath.Abs(requested)
		if err != nil {
			return "", fmt.Errorf("resolve log path: %w", err)
		}
		return path, nil
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	if stateHome := os.Getenv("XDG_STATE_HOME"); stateHome != "" {
		return filepath.Join(stateHome, "stormlight", "stormlight.log"), nil
	}
	switch runtime.GOOS {
	case "darwin":
		return filepath.Join(home, "Library", "Logs", "stormlight", "stormlight.log"), nil
	case "windows":
		if localAppData := os.Getenv("LOCALAPPDATA"); localAppData != "" {
			return filepath.Join(localAppData, "stormlight", "stormlight.log"), nil
		}
	}
	return filepath.Join(home, ".local", "state", "stormlight", "stormlight.log"), nil
}

func parseLevel(value string) (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "info":
		return slog.LevelInfo, nil
	case "debug":
		return slog.LevelDebug, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return 0, fmt.Errorf("invalid log level %q: use debug, info, warn, or error", value)
	}
}

func rotate(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("inspect log file: %w", err)
	}
	if info.Size() < maxLogSize {
		return nil
	}

	backup := path + ".1"
	if err := os.Remove(backup); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove old log backup: %w", err)
	}
	if err := os.Rename(path, backup); err != nil {
		return fmt.Errorf("rotate log file: %w", err)
	}
	return nil
}
