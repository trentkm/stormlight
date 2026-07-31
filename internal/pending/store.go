package pending

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

const (
	storeVersion         = 1
	defaultControllerTTL = 6 * time.Second
	defaultPollInterval  = 150 * time.Millisecond
	maxActionAge         = 24 * time.Hour
)

var ErrNoController = errors.New("no active Runstead controller")

type Store struct {
	dir           string
	controllerTTL time.Duration
	pollInterval  time.Duration
	now           func() time.Time
}

type requestFile struct {
	Version int    `json:"version"`
	Action  Action `json:"action"`
}

type resolutionFile struct {
	Version    int        `json:"version"`
	Resolution Resolution `json:"resolution"`
}

func NewStore() *Store {
	return NewStoreAt(actionsPath())
}

func NewStoreAt(dir string) *Store {
	return &Store{
		dir:           dir,
		controllerTTL: defaultControllerTTL,
		pollInterval:  defaultPollInterval,
		now:           time.Now,
	}
}

func (s *Store) Publish(action Action) (Action, error) {
	if strings.TrimSpace(s.dir) == "" {
		return Action{}, fmt.Errorf("pending action directory is unavailable")
	}
	if strings.TrimSpace(action.AgentID) == "" {
		return Action{}, fmt.Errorf("pending action agent id is required")
	}
	if action.Provider == "" {
		return Action{}, fmt.Errorf("pending action provider is required")
	}
	if action.Kind == "" {
		return Action{}, fmt.Errorf("pending action kind is required")
	}
	if len(action.Options) == 0 {
		return Action{}, fmt.Errorf("pending action requires at least one option")
	}
	seen := make(map[string]bool, len(action.Options))
	for _, option := range action.Options {
		if strings.TrimSpace(option.ID) == "" || strings.TrimSpace(option.Label) == "" {
			return Action{}, fmt.Errorf("pending action option id and label are required")
		}
		if seen[option.ID] {
			return Action{}, fmt.Errorf("duplicate pending action option %q", option.ID)
		}
		seen[option.ID] = true
	}

	if action.ID == "" {
		id, err := newActionID()
		if err != nil {
			return Action{}, err
		}
		action.ID = id
	}
	if !validActionID(action.ID) {
		return Action{}, fmt.Errorf("invalid pending action id")
	}
	if action.CreatedAt.IsZero() {
		action.CreatedAt = s.now().UTC()
	}

	if err := writeJSONAtomic(
		s.requestPath(action.ID),
		requestFile{Version: storeVersion, Action: action},
	); err != nil {
		return Action{}, fmt.Errorf("publish pending action: %w", err)
	}
	return action, nil
}

func (s *Store) List(ctx context.Context) ([]Action, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := s.Heartbeat(); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, fmt.Errorf("list pending actions: %w", err)
	}

	actions := make([]Action, 0)
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".request.json") {
			continue
		}
		id := strings.TrimSuffix(entry.Name(), ".request.json")
		if !validActionID(id) {
			continue
		}
		if _, err := os.Stat(s.resolutionPath(id)); err == nil {
			continue
		}

		request, err := s.readRequest(id)
		if err != nil {
			continue
		}
		if s.now().Sub(request.Action.CreatedAt) > maxActionAge {
			_ = s.Remove(id)
			continue
		}
		actions = append(actions, request.Action)
	}
	slices.SortStableFunc(actions, func(a, b Action) int {
		return a.CreatedAt.Compare(b.CreatedAt)
	})
	return actions, nil
}

func (s *Store) Resolve(ctx context.Context, actionID, optionID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !validActionID(actionID) {
		return fmt.Errorf("invalid pending action id")
	}
	request, err := s.readRequest(actionID)
	if err != nil {
		return err
	}
	validOption := false
	for _, option := range request.Action.Options {
		if option.ID == optionID {
			validOption = true
			break
		}
	}
	if !validOption {
		return fmt.Errorf("pending action option %q is unavailable", optionID)
	}

	resolution := Resolution{
		ActionID: actionID,
		OptionID: optionID,
		Created:  s.now().UTC(),
	}
	if err := writeJSONAtomic(
		s.resolutionPath(actionID),
		resolutionFile{Version: storeVersion, Resolution: resolution},
	); err != nil {
		return fmt.Errorf("resolve pending action: %w", err)
	}
	return nil
}

func (s *Store) Wait(ctx context.Context, actionID string) (Resolution, error) {
	if !validActionID(actionID) {
		return Resolution{}, fmt.Errorf("invalid pending action id")
	}
	active, err := s.ControllerActive()
	if err != nil {
		return Resolution{}, err
	}
	if !active {
		return Resolution{}, ErrNoController
	}

	ticker := time.NewTicker(s.pollInterval)
	defer ticker.Stop()
	for {
		resolution, found, err := s.readResolution(actionID)
		if err != nil {
			return Resolution{}, err
		}
		if found {
			return resolution, nil
		}
		active, err := s.ControllerActive()
		if err != nil {
			return Resolution{}, err
		}
		if !active {
			return Resolution{}, ErrNoController
		}

		select {
		case <-ctx.Done():
			return Resolution{}, ctx.Err()
		case <-ticker.C:
		}
	}
}

func (s *Store) Heartbeat() error {
	if strings.TrimSpace(s.dir) == "" {
		return fmt.Errorf("pending action directory is unavailable")
	}
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return fmt.Errorf("create pending action directory: %w", err)
	}
	path := filepath.Join(s.dir, "controller")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("open controller heartbeat: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close controller heartbeat: %w", err)
	}
	now := s.now()
	if err := os.Chtimes(path, now, now); err != nil {
		return fmt.Errorf("update controller heartbeat: %w", err)
	}
	return nil
}

func (s *Store) ControllerActive() (bool, error) {
	info, err := os.Stat(filepath.Join(s.dir, "controller"))
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("inspect controller heartbeat: %w", err)
	}
	return s.now().Sub(info.ModTime()) <= s.controllerTTL, nil
}

func (s *Store) Remove(actionID string) error {
	if !validActionID(actionID) {
		return fmt.Errorf("invalid pending action id")
	}
	var result error
	for _, path := range []string{s.requestPath(actionID), s.resolutionPath(actionID)} {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			result = errors.Join(result, err)
		}
	}
	return result
}

func (s *Store) readRequest(actionID string) (requestFile, error) {
	var request requestFile
	if err := readJSON(s.requestPath(actionID), &request); err != nil {
		return requestFile{}, fmt.Errorf("read pending action: %w", err)
	}
	if request.Version != storeVersion || request.Action.ID != actionID {
		return requestFile{}, fmt.Errorf("invalid pending action file")
	}
	return request, nil
}

func (s *Store) readResolution(actionID string) (Resolution, bool, error) {
	var response resolutionFile
	err := readJSON(s.resolutionPath(actionID), &response)
	if errors.Is(err, os.ErrNotExist) {
		return Resolution{}, false, nil
	}
	if err != nil {
		return Resolution{}, false, fmt.Errorf("read pending action response: %w", err)
	}
	if response.Version != storeVersion ||
		response.Resolution.ActionID != actionID ||
		strings.TrimSpace(response.Resolution.OptionID) == "" {
		return Resolution{}, false, fmt.Errorf("invalid pending action response")
	}
	return response.Resolution, true, nil
}

func (s *Store) requestPath(actionID string) string {
	return filepath.Join(s.dir, actionID+".request.json")
}

func (s *Store) resolutionPath(actionID string) string {
	return filepath.Join(s.dir, actionID+".response.json")
}

func writeJSONAtomic(path string, value any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".pending-*")
	if err != nil {
		return err
	}
	temporaryPath := file.Name()
	defer os.Remove(temporaryPath)

	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return err
	}
	if err := json.NewEncoder(file).Encode(value); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryPath, path)
}

func readJSON(path string, value any) error {
	content, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(content, value)
}

func newActionID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", fmt.Errorf("create pending action id: %w", err)
	}
	return hex.EncodeToString(value[:]), nil
}

func validActionID(value string) bool {
	if len(value) != 32 {
		return false
	}
	for _, char := range value {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return false
		}
	}
	return true
}

func actionsPath() string {
	if configured := os.Getenv("RUNSTEAD_ACTIONS_DIR"); configured != "" {
		return configured
	}
	if runtimeDir := os.Getenv("XDG_RUNTIME_DIR"); runtimeDir != "" {
		return filepath.Join(runtimeDir, "runstead", "actions")
	}
	if stateHome := os.Getenv("XDG_STATE_HOME"); stateHome != "" {
		return filepath.Join(stateHome, "runstead", "actions")
	}
	home, err := os.UserHomeDir()
	if err == nil {
		return filepath.Join(home, ".local", "state", "runstead", "actions")
	}
	cache, err := os.UserCacheDir()
	if err == nil {
		return filepath.Join(cache, "runstead", "actions")
	}
	return ""
}
