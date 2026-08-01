package tmux

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/trentkm/stormlight/internal/agent"
	"github.com/trentkm/stormlight/internal/diagnostic"
	"github.com/trentkm/stormlight/internal/session"
	"github.com/trentkm/stormlight/internal/workspace"
)

const (
	defaultSession          = "stormlight-agents"
	fieldSeparator          = "\x1f"
	cleanupTimeout          = 5 * time.Second
	returnBindingKey        = "Q"
	returnBindingNote       = "Return from Stormlight"
	returnTargetOption      = "@stormlight_return_target"
	returnBindingFormat     = `#{?#{==:#{@stormlight_id},},display-message "Not a Stormlight window",#{?#{==:#{@stormlight_return_target},},detach-client,switch-client -t "#{@stormlight_return_target}"}}`
	returnMouseKey          = "MouseDown1StatusRight"
	returnAction            = `#{?#{==:#{@stormlight_return_target},},detach-client,switch-client -t "#{@stormlight_return_target}"}`
	statusStyleBaseOption   = "@stormlight_status_style_base"
	statusStylePrefixOption = "@stormlight_status_style_prefix"
	statusLeftBaseOption    = "@stormlight_status_left_base"
	statusLeftPrefixOption  = "@stormlight_status_left_prefix"
	statusLeftLengthOption  = "@stormlight_status_left_length_base"
	prefixFeedbackOption    = "@stormlight_prefix_feedback"
	prefixFeedbackVersion   = "1"
	prefixStatusStyle       = "bg=#e5c07b,fg=#1f2328,bold"
	prefixStatusLeft        = " PREFIX  [Q] return  [?] all keys "
	dynamicStatusStyle      = `#{?client_prefix,#{E:@stormlight_status_style_prefix},#{E:@stormlight_status_style_base}}`
	dynamicStatusLeft       = `#{?client_prefix,#{E:@stormlight_status_left_prefix},#{E:@stormlight_status_left_base}}`
	basePaneFieldCount      = 10
	metadataFieldCount      = 18
)

var agentMetadataFields = [metadataFieldCount]string{
	"id",
	"provider",
	"task",
	"summary",
	"cwd",
	"created_at",
	"activity",
	"attention",
	"pane",
	"workspace_id",
	"workspace_kind",
	"workspace_name",
	"workspace_root",
	"execution_root",
	"component_name",
	"component_root",
	"workspace_metadata",
	"mode",
}

type Runtime struct {
	runner      Runner
	sessionName string
	executable  string
	socket      string
	returnKeys  []string
}

var _ session.Runtime = (*Runtime)(nil)

func NewRuntime(runner Runner, sessionName string) (*Runtime, error) {
	if sessionName == "" {
		sessionName = defaultSession
	}
	executable, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("resolve Stormlight executable: %w", err)
	}
	runtime := &Runtime{
		runner:      runner,
		sessionName: sessionName,
		executable:  executable,
	}
	if source, ok := runner.(interface{ Socket() string }); ok {
		runtime.socket = source.Socket()
	}
	return runtime, nil
}

// SetReturnKeys overrides the single-press return-to-dashboard keys
// installed in tmux's root table (default C-6 and C-^).
func (r *Runtime) SetReturnKeys(keys []string) {
	r.returnKeys = keys
}

type rootBinding struct {
	key         string
	passthrough string
}

func (r *Runtime) effectiveReturnKeys() []string {
	if len(r.returnKeys) > 0 {
		return r.returnKeys
	}
	return []string{"C-6", "C-^"}
}

func (r *Runtime) statusRightHint() string {
	return " " + r.effectiveReturnKeys()[0] + " ⏎ dashboard "
}

func (r *Runtime) ListAgents(ctx context.Context) ([]agent.Agent, error) {
	out, err := r.runner.Run(ctx, nil, "list-panes", "-a", "-F", paneListFormat())
	if err != nil {
		if isNoServerError(err) {
			return nil, nil
		}
		return nil, err
	}

	var agents []agent.Agent
	for _, line := range strings.Split(out, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		managedAgent, ok := parseAgent(line)
		if ok {
			agents = append(agents, managedAgent)
		}
	}
	agent.Sort(agents)
	return agents, nil
}

func paneListFormat() string {
	fields := []string{
		"#{session_name}",
		"#{window_id}",
		"#{window_index}",
		"#{window_name}",
		"#{pane_id}",
		"#{pane_current_command}",
		"#{pane_current_path}",
		"#{pane_title}",
		"#{pane_dead}",
		"#{pane_dead_status}",
	}
	for _, name := range agentMetadataFields {
		fields = append(fields, "#{@stormlight_"+name+"}")
	}
	return strings.Join(fields, fieldSeparator)
}

func (r *Runtime) Dispatch(ctx context.Context, req session.DispatchRequest) (agent.Agent, error) {
	if strings.TrimSpace(req.Task) == "" {
		return agent.Agent{}, fmt.Errorf("task cannot be empty")
	}
	if req.Launch.Path == "" {
		return agent.Agent{}, fmt.Errorf("provider launch command is empty")
	}

	cwd, err := normalizeCwd(req.Cwd)
	if err != nil {
		return agent.Agent{}, err
	}

	id, err := newID()
	if err != nil {
		return agent.Agent{}, fmt.Errorf("create agent id: %w", err)
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = windowName(req.Provider, req.Task)
	}

	target, err := r.createWindow(ctx, name, cwd)
	if err != nil {
		return agent.Agent{}, err
	}

	createdAt := time.Now().UTC()
	options := map[string]string{
		"@stormlight_id":         id,
		"@stormlight_provider":   string(req.Provider),
		"@stormlight_task":       metadataValue(req.Task),
		"@stormlight_summary":    metadataValue(req.Task),
		"@stormlight_cwd":        cwd,
		"@stormlight_created_at": strconv.FormatInt(createdAt.Unix(), 10),
		"@stormlight_activity":   string(agent.ActivityStarting),
		"@stormlight_attention":  "",
		"@stormlight_pane":       target.paneID,
		"@stormlight_mode":       string(req.Mode),
	}
	workspaceOptions, err := encodeWorkspaceOptions(req.Workspace)
	if err != nil {
		r.cleanupWindow(target.windowID)
		return agent.Agent{}, err
	}
	for key, value := range workspaceOptions {
		options[key] = value
	}
	for key, value := range options {
		if _, err := r.runner.Run(ctx, nil, "set-option", "-w", "-t", target.windowID, key, value); err != nil {
			r.cleanupWindow(target.windowID)
			return agent.Agent{}, fmt.Errorf("set %s metadata: %w", key, err)
		}
	}
	if _, err := r.runner.Run(ctx, nil, "set-option", "-w", "-t", target.windowID, "remain-on-exit", "on"); err != nil {
		r.cleanupWindow(target.windowID)
		return agent.Agent{}, fmt.Errorf("enable pane persistence: %w", err)
	}
	payload, err := encodeLaunch(req.Launch)
	if err != nil {
		r.cleanupWindow(target.windowID)
		return agent.Agent{}, err
	}
	commandArgs := []string{
		r.executable,
	}
	if r.socket != "" {
		commandArgs = append(commandArgs, "--tmux-socket", r.socket)
	}
	commandArgs = append(commandArgs,
		"--session", r.sessionName,
		"_run",
		"--id", id,
		"--window", target.windowID,
		"--cwd", cwd,
		"--launch", payload,
	)
	command := shellJoin(commandArgs)
	if _, err := r.runner.Run(ctx, nil, "respawn-pane", "-k", "-t", target.paneID, "-c", cwd, command); err != nil {
		r.cleanupWindow(target.windowID)
		return agent.Agent{}, fmt.Errorf("start provider: %w", err)
	}

	return agent.Agent{
		ID:          id,
		Provider:    req.Provider,
		Name:        name,
		Task:        req.Task,
		Summary:     req.Task,
		Cwd:         cwd,
		CreatedAt:   createdAt,
		Activity:    agent.ActivityStarting,
		TmuxSession: r.sessionName,
		WindowID:    target.windowID,
		PaneID:      target.paneID,
		ProcessLive: true,
		Workspace:   req.Workspace,
	}, nil
}

func (r *Runtime) Capture(ctx context.Context, id string, lines int) (string, error) {
	managedAgent, err := r.FindAgent(ctx, id)
	if err != nil {
		return "", err
	}
	if lines <= 0 {
		lines = 100
	}
	args := []string{"capture-pane", "-p", "-e", "-t", managedAgent.PaneID}
	if !managedAgent.ProcessLive {
		args = append(args, "-S", fmt.Sprintf("-%d", lines))
	}
	return r.runner.Run(ctx, nil, args...)
}

func (r *Runtime) Attach(ctx context.Context, id string) (session.AttachResult, error) {
	managedAgent, err := r.FindAgent(ctx, id)
	if err != nil {
		return session.AttachResult{}, err
	}

	insideTmux := insideTmuxServer(r.socket)
	returnTarget := ""
	if insideTmux {
		sourcePane := os.Getenv("TMUX_PANE")
		if sourcePane == "" {
			return session.AttachResult{}, fmt.Errorf("find return target: TMUX_PANE is not set")
		}
		returnTarget, err = r.runner.Run(ctx, nil,
			"display-message", "-p", "-t", sourcePane, "#{session_id}",
		)
		if err != nil {
			return session.AttachResult{}, fmt.Errorf("find return target: %w", err)
		}
		if returnTarget == "" {
			return session.AttachResult{}, fmt.Errorf("find return target: tmux returned an empty session id")
		}
	}
	if err := r.configureReturn(
		ctx,
		managedAgent.TmuxSession,
		managedAgent.WindowID,
		returnTarget,
	); err != nil {
		return session.AttachResult{}, err
	}

	diagnostic.Logger().Info("opening agent",
		"agent_id", managedAgent.ID,
		"window_id", managedAgent.WindowID,
		"inside_tmux", insideTmux,
		"return_target", returnTarget,
	)
	if insideTmux {
		if _, err := r.runner.Run(ctx, nil, "switch-client", "-t", managedAgent.WindowID); err != nil {
			return session.AttachResult{}, fmt.Errorf("switch tmux client: %w", err)
		}
		return session.AttachResult{}, nil
	}

	if _, err := r.runner.Run(ctx, nil, "select-window", "-t", managedAgent.WindowID); err != nil {
		return session.AttachResult{}, fmt.Errorf("select agent window: %w", err)
	}
	args := make([]string, 0, 5)
	if r.socket != "" {
		args = append(args, "-L", r.socket)
	}
	args = append(args, "attach-session", "-t", managedAgent.TmuxSession)
	command := exec.Command("tmux", args...)
	command.Env = withoutTmuxEnvironment(os.Environ())
	return session.AttachResult{Command: command}, nil
}

func insideTmuxServer(socket string) bool {
	current := os.Getenv("TMUX")
	if current == "" {
		return false
	}
	if socket == "" {
		return true
	}

	lastComma := strings.LastIndex(current, ",")
	if lastComma < 0 {
		return false
	}
	previousComma := strings.LastIndex(current[:lastComma], ",")
	if previousComma < 0 {
		return false
	}
	return filepath.Base(current[:previousComma]) == socket
}

func (r *Runtime) configureReturn(ctx context.Context, sessionName, windowID, target string) error {
	// Querying a single key (list-keys -T prefix Q) silently returns nothing
	// on tmux 3.6, so list the whole table and find the key ourselves.
	listing, err := r.runner.Run(ctx, nil, "list-keys", "-T", "prefix")
	if err != nil {
		return fmt.Errorf("inspect tmux return binding: %w", err)
	}
	if binding, bound := tableBinding(listing, "prefix", returnBindingKey); bound &&
		!strings.Contains(binding, "@stormlight_") {
		diagnostic.Logger().Warn("foreign tmux return binding",
			"key", returnBindingKey,
			"binding", binding,
		)
		return fmt.Errorf(
			"tmux prefix %s is bound outside Stormlight; not replacing it",
			returnBindingKey,
		)
	}

	if _, err := r.runner.Run(ctx, nil,
		"bind-key", "-T", "prefix", "-N", returnBindingNote,
		returnBindingKey, "run-shell", "-C", returnBindingFormat,
	); err != nil {
		return fmt.Errorf("install tmux return binding: %w", err)
	}
	r.configureRootReturn(ctx)
	if err := r.configurePrefixFeedback(ctx, sessionName); err != nil {
		diagnostic.Logger().Warn("tmux prefix feedback unavailable", "error", err)
	}
	if _, err := r.runner.Run(ctx, nil,
		"set-option", "-w", "-t", windowID, returnTargetOption, target,
	); err != nil {
		return fmt.Errorf("set tmux return target: %w", err)
	}
	return nil
}

// configureRootReturn installs single-press escapes from agent windows: C-6
// and C-^ (the same physical Ctrl-6 press under extended-keys and legacy
// terminals — vim's alternate-buffer toggle), plus a click on the
// status-right hint. Root-table keys reach tmux before the pane application,
// so no prefix is needed; in non-Stormlight windows the key or mouse event
// is forwarded to the pane untouched. Failures are non-fatal because prefix
// Q still works.
func (r *Runtime) configureRootReturn(ctx context.Context) {
	listing, err := r.runner.Run(ctx, nil, "list-keys", "-T", "root")
	if err != nil {
		diagnostic.Logger().Warn("cannot inspect tmux root bindings", "error", err)
		return
	}
	bindings := make([]rootBinding, 0, len(r.effectiveReturnKeys())+1)
	for _, key := range r.effectiveReturnKeys() {
		bindings = append(bindings, rootBinding{key, "send-keys " + key})
	}
	bindings = append(bindings, rootBinding{returnMouseKey, "send-keys -M"})
	for _, binding := range bindings {
		if current, bound := tableBinding(listing, "root", binding.key); bound &&
			!strings.Contains(current, "@stormlight_") {
			diagnostic.Logger().Warn("foreign tmux root binding left in place",
				"key", binding.key,
				"binding", current,
			)
			continue
		}
		format := `#{?#{==:#{@stormlight_id},},` +
			binding.passthrough + `,` + returnAction + `}`
		if _, err := r.runner.Run(ctx, nil,
			"bind-key", "-T", "root", "-N", returnBindingNote,
			binding.key, "run-shell", "-C", format,
		); err != nil {
			diagnostic.Logger().Warn("cannot install tmux return binding",
				"key", binding.key,
				"error", err,
			)
		}
	}
}

func (r *Runtime) configurePrefixFeedback(ctx context.Context, sessionName string) error {
	version, err := r.runner.Run(ctx, nil,
		"show-options", "-qv", "-t", sessionName, prefixFeedbackOption,
	)
	if err != nil {
		return fmt.Errorf("read prefix feedback version: %w", err)
	}

	baseLength := 0
	if version != prefixFeedbackVersion {
		baseStyle, err := r.readSessionOption(ctx, sessionName, "status-style")
		if err != nil {
			return err
		}
		baseLeft, err := r.readSessionOption(ctx, sessionName, "status-left")
		if err != nil {
			return err
		}
		baseLengthText, err := r.readSessionOption(
			ctx,
			sessionName,
			"status-left-length",
		)
		if err != nil {
			return err
		}
		baseLength, err = strconv.Atoi(baseLengthText)
		if err != nil {
			return fmt.Errorf("parse status left length %q: %w", baseLengthText, err)
		}
		baseOptions := []struct {
			name  string
			value string
		}{
			{statusStyleBaseOption, baseStyle},
			{statusLeftBaseOption, baseLeft},
			{statusLeftLengthOption, baseLengthText},
			{prefixFeedbackOption, prefixFeedbackVersion},
		}
		for _, option := range baseOptions {
			if _, err := r.runner.Run(ctx, nil,
				"set-option", "-t", sessionName, option.name, option.value,
			); err != nil {
				return fmt.Errorf("set %s: %w", option.name, err)
			}
		}
	} else {
		baseLengthText, err := r.runner.Run(ctx, nil,
			"show-options", "-qv", "-t", sessionName, statusLeftLengthOption,
		)
		if err != nil {
			return fmt.Errorf("read saved status left length: %w", err)
		}
		baseLength, err = strconv.Atoi(baseLengthText)
		if err != nil {
			return fmt.Errorf("parse saved status left length %q: %w", baseLengthText, err)
		}
	}

	options := []struct {
		name  string
		value string
	}{
		{statusStylePrefixOption, prefixStatusStyle},
		{statusLeftPrefixOption, prefixStatusLeft},
		{"status-style", dynamicStatusStyle},
		{"status-left", dynamicStatusLeft},
		{"status-left-length", strconv.Itoa(max(baseLength, len(prefixStatusLeft)))},
		{"status-right", r.statusRightHint()},
		{"status-right-length", strconv.Itoa(len(r.statusRightHint()))},
	}
	for _, option := range options {
		if _, err := r.runner.Run(ctx, nil,
			"set-option", "-t", sessionName, option.name, option.value,
		); err != nil {
			return fmt.Errorf("set %s: %w", option.name, err)
		}
	}
	return nil
}

func (r *Runtime) readSessionOption(ctx context.Context, sessionName, name string) (string, error) {
	const delimiter = "|"
	format := delimiter + "#{" + name + "}" + delimiter
	value, err := r.runner.Run(ctx, nil,
		"display-message", "-p", "-t", sessionName, format,
	)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", name, err)
	}
	if len(value) < 2 || !strings.HasPrefix(value, delimiter) || !strings.HasSuffix(value, delimiter) {
		return "", fmt.Errorf("read %s: tmux returned malformed output", name)
	}
	return value[1 : len(value)-1], nil
}

func (r *Runtime) Send(ctx context.Context, id, message string) error {
	managedAgent, err := r.FindAgent(ctx, id)
	if err != nil {
		return err
	}
	if !managedAgent.ProcessLive {
		return fmt.Errorf("agent %s is not running", id)
	}
	buffer := "stormlight-" + id
	if _, err := r.runner.Run(ctx, []byte(message), "load-buffer", "-b", buffer, "-"); err != nil {
		return err
	}
	if _, err := r.runner.Run(ctx, nil,
		"paste-buffer", "-d", "-b", buffer, "-t", managedAgent.PaneID,
	); err != nil {
		return err
	}
	_, err = r.runner.Run(ctx, nil, "send-keys", "-t", managedAgent.PaneID, "Enter")
	if err == nil {
		_ = r.Update(ctx, id, session.Update{Activity: agent.ActivityWorking})
	}
	return err
}

func (r *Runtime) Interrupt(ctx context.Context, id string) error {
	managedAgent, err := r.FindAgent(ctx, id)
	if err != nil {
		return err
	}
	if _, err := r.runner.Run(ctx, nil, "send-keys", "-t", managedAgent.PaneID, "C-c"); err != nil {
		return err
	}
	return r.Update(ctx, id, session.Update{Activity: agent.ActivityIdle})
}

func (r *Runtime) Delete(ctx context.Context, id string) error {
	managedAgent, err := r.FindAgent(ctx, id)
	if err != nil {
		return err
	}
	return r.killWindow(ctx, managedAgent.WindowID)
}

func (r *Runtime) Rename(ctx context.Context, id, name string) error {
	name = metadataValue(name)
	if name == "" {
		return fmt.Errorf("agent name cannot be empty")
	}
	managedAgent, err := r.FindAgent(ctx, id)
	if err != nil {
		return err
	}
	_, err = r.runner.Run(ctx, nil,
		"rename-window", "-t", managedAgent.WindowID, name,
	)
	return err
}

func (r *Runtime) FindAgent(ctx context.Context, id string) (agent.Agent, error) {
	agents, err := r.ListAgents(ctx)
	if err != nil {
		return agent.Agent{}, err
	}
	var match *agent.Agent
	for i := range agents {
		if agents[i].ID == id || strings.HasPrefix(agents[i].ID, id) {
			if match != nil {
				return agent.Agent{}, fmt.Errorf("agent id %q is ambiguous", id)
			}
			copy := agents[i]
			match = &copy
		}
	}
	if match == nil {
		return agent.Agent{}, fmt.Errorf("agent %q not found", id)
	}
	return *match, nil
}

func (r *Runtime) Update(ctx context.Context, id string, update session.Update) error {
	managedAgent, err := r.FindAgent(ctx, id)
	if err != nil {
		return err
	}
	values := map[string]string{}
	stormlightID, err := r.runner.Run(ctx, nil,
		"show-options", "-qv", "-w", "-t", managedAgent.WindowID, "@stormlight_id",
	)
	if err != nil {
		return fmt.Errorf("inspect Stormlight metadata: %w", err)
	}
	if stormlightID == "" {
		values, err = encodeAgentOptions(managedAgent)
		if err != nil {
			return err
		}
	}
	if update.Activity != "" {
		values["@stormlight_activity"] = string(update.Activity)
	}
	if update.Attention != "" || update.Activity != "" {
		attention := update.Attention
		if attention == agent.AttentionWaiting && managedAgent.Attention.Urgent() {
			// Waiting is a floor, not a ceiling: the delayed idle signal
			// must never downgrade an urgent question/approval/auth state
			// set when the turn ended.
			attention = managedAgent.Attention
		}
		values["@stormlight_attention"] = string(attention)
	}
	if strings.TrimSpace(update.Summary) != "" {
		values["@stormlight_summary"] = metadataValue(update.Summary)
	}
	for key, value := range values {
		if _, err := r.runner.Run(ctx, nil,
			"set-option", "-w", "-t", managedAgent.WindowID, key, value,
		); err != nil {
			return err
		}
	}
	return nil
}

func (r *Runtime) SetWorkspace(
	ctx context.Context,
	id string,
	value workspace.Context,
) error {
	managedAgent, err := r.FindAgent(ctx, id)
	if err != nil {
		return err
	}
	options, err := encodeWorkspaceOptions(value)
	if err != nil {
		return err
	}
	for key, item := range options {
		if _, err := r.runner.Run(ctx, nil,
			"set-option", "-w", "-t", managedAgent.WindowID, key, item,
		); err != nil {
			return fmt.Errorf("set %s metadata: %w", key, err)
		}
	}
	return nil
}

func (r *Runtime) SessionName() string {
	return r.sessionName
}

type windowTarget struct {
	windowID string
	paneID   string
}

func (r *Runtime) createWindow(ctx context.Context, name, cwd string) (windowTarget, error) {
	format := "#{window_id}" + fieldSeparator + "#{pane_id}"
	_, err := r.runner.Run(ctx, nil, "has-session", "-t", r.sessionName)
	var out string
	if err != nil {
		out, err = r.runner.Run(ctx, nil,
			"new-session", "-d", "-P", "-F", format,
			"-s", r.sessionName, "-n", name, "-c", cwd,
		)
		if err == nil {
			_, err = r.runner.Run(ctx, nil, "set-option", "-t", r.sessionName, "@stormlight_managed", "1")
			if err != nil {
				r.cleanupSession()
			}
		}
	} else {
		marker, markerErr := r.runner.Run(ctx, nil,
			"show-options", "-qv", "-t", r.sessionName, "@stormlight_managed",
		)
		if markerErr != nil {
			return windowTarget{}, fmt.Errorf("inspect managed tmux session: %w", markerErr)
		}
		if marker != "1" {
			return windowTarget{}, fmt.Errorf(
				"tmux session %q already exists and is not managed by Stormlight; choose another with --session",
				r.sessionName,
			)
		}
		out, err = r.runner.Run(ctx, nil,
			"new-window", "-d", "-P", "-F", format,
			"-t", r.sessionName+":", "-n", name, "-c", cwd,
		)
	}
	if err != nil {
		return windowTarget{}, fmt.Errorf("create tmux window: %w", err)
	}
	parts := strings.SplitN(out, fieldSeparator, 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return windowTarget{}, fmt.Errorf("tmux returned an invalid window target %q", out)
	}
	return windowTarget{windowID: parts[0], paneID: parts[1]}, nil
}

func (r *Runtime) killWindow(ctx context.Context, windowID string) error {
	_, err := r.runner.Run(ctx, nil, "kill-window", "-t", windowID)
	return err
}

func (r *Runtime) cleanupWindow(windowID string) {
	ctx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
	defer cancel()
	_ = r.killWindow(ctx, windowID)
}

func (r *Runtime) cleanupSession() {
	ctx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
	defer cancel()
	_, _ = r.runner.Run(ctx, nil, "kill-session", "-t", r.sessionName)
}

func parseAgent(line string) (agent.Agent, bool) {
	parts := strings.Split(line, fieldSeparator)
	expectedFields := basePaneFieldCount + metadataFieldCount
	if len(parts) != expectedFields {
		return agent.Agent{}, false
	}

	core := parts[basePaneFieldCount:]
	if core[0] == "" || parts[4] != core[8] {
		return agent.Agent{}, false
	}

	windowIndex, _ := strconv.Atoi(parts[2])
	createdUnix, _ := strconv.ParseInt(core[5], 10, 64)
	createdAt := time.Unix(createdUnix, 0).UTC()
	if createdUnix == 0 {
		createdAt = time.Time{}
	}
	paneDead := parts[8] == "1"
	var exitCode *int
	if paneDead && parts[9] != "" {
		code, err := strconv.Atoi(parts[9])
		if err == nil {
			exitCode = &code
		}
	}

	activity := agent.Activity(core[6])
	if paneDead {
		switch {
		case activity == agent.ActivityStopped:
		case exitCode != nil && (*exitCode == 130 || *exitCode == 143):
			activity = agent.ActivityStopped
		case exitCode != nil && *exitCode == 0:
			activity = agent.ActivityCompleted
		default:
			activity = agent.ActivityFailed
		}
	} else if activity == "" {
		activity = agent.ActivityWorking
	}

	return agent.Agent{
		ID:          core[0],
		Provider:    agent.Provider(core[1]),
		Name:        parts[3],
		Task:        core[2],
		Summary:     core[3],
		Cwd:         core[4],
		CreatedAt:   createdAt,
		Activity:    activity,
		Attention:   agent.Attention(core[7]),
		TmuxSession: parts[0],
		WindowID:    parts[1],
		WindowIndex: windowIndex,
		PaneID:      parts[4],
		Command:     parts[5],
		PaneTitle:   parts[7],
		ProcessLive: !paneDead,
		ExitCode:    exitCode,
		Mode:        agent.PermissionMode(core[17]),
		Workspace: workspace.Context{
			ID:            core[9],
			Kind:          core[10],
			Name:          core[11],
			Root:          core[12],
			ExecutionRoot: core[13],
			ComponentName: core[14],
			ComponentRoot: core[15],
			Metadata:      decodeWorkspaceMetadata(core[16]),
		},
	}, true
}

func encodeAgentOptions(managedAgent agent.Agent) (map[string]string, error) {
	createdAt := ""
	if !managedAgent.CreatedAt.IsZero() {
		createdAt = strconv.FormatInt(managedAgent.CreatedAt.Unix(), 10)
	}
	options := map[string]string{
		"@stormlight_id":         managedAgent.ID,
		"@stormlight_provider":   string(managedAgent.Provider),
		"@stormlight_task":       metadataValue(managedAgent.Task),
		"@stormlight_summary":    metadataValue(managedAgent.Summary),
		"@stormlight_cwd":        managedAgent.Cwd,
		"@stormlight_created_at": createdAt,
		"@stormlight_activity":   string(managedAgent.Activity),
		"@stormlight_attention":  string(managedAgent.Attention),
		"@stormlight_pane":       managedAgent.PaneID,
		"@stormlight_mode":       string(managedAgent.Mode),
	}
	workspaceOptions, err := encodeWorkspaceOptions(managedAgent.Workspace)
	if err != nil {
		return nil, err
	}
	for key, value := range workspaceOptions {
		options[key] = value
	}
	return options, nil
}

func encodeWorkspaceOptions(value workspace.Context) (map[string]string, error) {
	metadata := ""
	if len(value.Metadata) > 0 {
		encoded, err := json.Marshal(value.Metadata)
		if err != nil {
			return nil, fmt.Errorf("encode workspace metadata: %w", err)
		}
		metadata = base64.RawURLEncoding.EncodeToString(encoded)
	}
	return map[string]string{
		"@stormlight_workspace_id":       value.ID,
		"@stormlight_workspace_kind":     value.Kind,
		"@stormlight_workspace_name":     value.Name,
		"@stormlight_workspace_root":     value.Root,
		"@stormlight_execution_root":     value.ExecutionRoot,
		"@stormlight_component_name":     value.ComponentName,
		"@stormlight_component_root":     value.ComponentRoot,
		"@stormlight_workspace_metadata": metadata,
	}, nil
}

func decodeWorkspaceMetadata(encoded string) map[string]string {
	if encoded == "" {
		return nil
	}
	data, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return nil
	}
	var metadata map[string]string
	if err := json.Unmarshal(data, &metadata); err != nil {
		return nil
	}
	return metadata
}

func normalizeCwd(cwd string) (string, error) {
	if strings.TrimSpace(cwd) == "" {
		var err error
		cwd, err = os.Getwd()
		if err != nil {
			return "", fmt.Errorf("get current directory: %w", err)
		}
	}
	absolute, err := filepath.Abs(cwd)
	if err != nil {
		return "", fmt.Errorf("resolve working directory: %w", err)
	}
	info, err := os.Stat(absolute)
	if err != nil {
		return "", fmt.Errorf("working directory: %w", err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("working directory %s is not a directory", absolute)
	}
	return absolute, nil
}

func newID() (string, error) {
	var data [8]byte
	if _, err := rand.Read(data[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(data[:]), nil
}

func windowName(providerID agent.Provider, task string) string {
	prefix := map[agent.Provider]string{
		agent.ProviderClaude: "cl",
		agent.ProviderCodex:  "cx",
		agent.ProviderShell:  "sh",
	}[providerID]
	if prefix == "" {
		prefix = "ag"
	}

	var words []string
	var current strings.Builder
	for _, r := range strings.ToLower(task) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			current.WriteRune(r)
		default:
			if current.Len() > 0 {
				words = append(words, current.String())
				current.Reset()
			}
		}
		if len(words) == 4 {
			break
		}
	}
	if current.Len() > 0 && len(words) < 4 {
		words = append(words, current.String())
	}
	slug := strings.Join(words, "-")
	if slug == "" {
		slug = "agent"
	}
	const maxSlug = 24
	if len(slug) > maxSlug {
		slug = strings.TrimRight(slug[:maxSlug], "-")
	}
	return prefix + "-" + slug
}

func metadataValue(value string) string {
	return strings.Join(strings.Fields(value), " ")
}

func encodeLaunch(launch session.Launch) (string, error) {
	data, err := json.Marshal(launch)
	if err != nil {
		return "", fmt.Errorf("encode provider launch: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(data), nil
}

func DecodeLaunch(encoded string) (session.Launch, error) {
	data, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return session.Launch{}, fmt.Errorf("decode provider launch: %w", err)
	}
	var launch session.Launch
	if err := json.Unmarshal(data, &launch); err != nil {
		return session.Launch{}, fmt.Errorf("decode provider launch: %w", err)
	}
	if launch.Path == "" {
		return session.Launch{}, fmt.Errorf("provider launch path is empty")
	}
	return launch, nil
}

func shellJoin(args []string) string {
	quoted := make([]string, len(args))
	for i, arg := range args {
		if arg == "" {
			quoted[i] = "''"
			continue
		}
		quoted[i] = "'" + strings.ReplaceAll(arg, "'", "'\"'\"'") + "'"
	}
	return strings.Join(quoted, " ")
}

func isNoServerError(err error) bool {
	text := strings.ToLower(err.Error())
	return strings.Contains(text, "no server running") ||
		strings.Contains(text, "failed to connect to server") ||
		strings.Contains(text, "error connecting to") ||
		strings.Contains(text, "no sessions")
}

// tableBinding finds the bind-key line for a key in `list-keys -T <table>`
// output. Lines look like `bind-key [-r] -T <table> <key> <command...>`.
func tableBinding(listing, table, key string) (string, bool) {
	for _, line := range strings.Split(listing, "\n") {
		fields := strings.Fields(line)
		for i := 0; i+2 < len(fields); i++ {
			if fields[i] != "-T" {
				continue
			}
			if fields[i+1] == table && fields[i+2] == key {
				return strings.TrimSpace(line), true
			}
			break
		}
	}
	return "", false
}
