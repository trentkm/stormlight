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
	statusVersionOption     = "@stormlight_status_version"
	statusVersion           = "3"
	prefixStatusStyle       = "bg=#e5c07b,fg=#1f2328,bold"
	prefixStatusLeft        = " PREFIX  [Q] return  [?] all keys "
	// The agents session lives on the Stormlight-owned server, so its
	// status bar carries the dashboard's identity: a deep sapphire band
	// with the glint-and-wordmark status-left, echoing the header.
	baseStatusStyle = "bg=#1B2740,fg=#C8D6F2"
	// A window inside an agent is a place, and the bar names it the way the
	// dashboard does: workspace › worktree › agent. Only the runtime's own
	// windows carry @stormlight_id; anything else on the band (the dashboard's
	// own host session, a stray shell) falls back to the session name.
	//
	// Commas inside #[...] have to be escaped as #, — an unescaped one ends
	// the surrounding #{?...} branch and swallows the rest of the format.
	agentStatusLineage = `#{?#{@stormlight_workspace_name},` +
		statusWorkspaceSegment + statusTailSegment + `,}` +
		`#[fg=#E8EEF9#,bold]#{window_name}`
	// Each segment is capped so a verbose workspace name cannot crowd out the
	// agent, and each is dropped outright once the client is too narrow to
	// hold it: the worktree below 90 columns, the workspace below 60. The
	// agent's own name never drops — it is the answer the bar exists to give.
	statusWorkspaceSegment = `#{?#{e|>=:#{client_width},60},` +
		`#[fg=#8FA6CC]#{=/16/…:@stormlight_workspace_name}#[fg=#55698F] › ,}`
	statusTailSegment = `#{?#{e|>=:#{client_width},90},` +
		`#{?#{@stormlight_workspace_tail},` +
		`#[fg=#8FA6CC]#{=/16/…:@stormlight_workspace_tail}#[fg=#55698F] › ,},}`
	// The glint drops back to #[default] immediately: tmux attributes latch
	// until something clears them, so a bold that stayed on would make the
	// whole band bold and flatten the lineage into one weight.
	baseStatusLeft = ` #[fg=#7DCFFF#,bold]✦#[default] #{?#{@stormlight_id},` +
		agentStatusLineage + `,#[fg=#E8EEF9#,bold]#{session_name}} `
	// status-left is the whole bar now, so its own length cap stays out of the
	// way; what keeps it off the return hint is the render-time truncation in
	// dynamicStatusLeft, which subtracts the hint's width from the client's.
	baseStatusLeftLength = 400
	dynamicStatusStyle   = `#{?client_prefix,#{E:@stormlight_status_style_prefix},#{E:@stormlight_status_style_base}}`
	dynamicStatusLeft    = `#{?client_prefix,#{E:@stormlight_status_left_prefix},` +
		`#{T;=/#{e|-:#{client_width},#{status-right-length}}:@stormlight_status_left_base}}`
	// Stormlight's windows are its agents, and the dashboard is where you
	// choose between them — listing them again on every agent's status bar
	// only pushes the lineage of the one you are actually in off the band. So
	// the bar is left and right sections with no window list between them.
	// status-format is a session option, which is what makes this safe: the
	// managed session may share a server with the user's own sessions, and
	// they keep tmux's default bar.
	statusFormat = `#[align=left range=left #{E:status-left-style}]#[push-default]` +
		`#{T;=/#{status-left-length}:status-left}` +
		`#[pop-default]#[norange default]` +
		`#[align=right range=right #{E:status-right-style}]#[push-default]` +
		`#{T;=/#{status-right-length}:status-right}` +
		`#[pop-default]#[norange default]`
	basePaneFieldCount = 10
	metadataFieldCount = 20
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
	"transcript_path",
	"mark",
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
	// A user-chosen name becomes the tmux window name verbatim, so it goes
	// through the same whitespace collapse as Rename — a newline or tab in
	// a window name confuses every tmux format string that prints it.
	name := metadataValue(req.Name)
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
		"@stormlight_mark":       "",
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
	if lines == 0 {
		lines = 100
	}
	// Negative budgets ask for the pane's entire history.
	start := "-"
	if lines > 0 {
		start = fmt.Sprintf("-%d", lines)
	}
	// -J rejoins lines the pane hard-wrapped at its own width, so the
	// dashboard re-wraps whole logical lines instead of the pane's
	// fragments — indentation survives the width difference.
	return r.runner.Run(ctx, nil,
		"capture-pane", "-p", "-e", "-J", "-t", managedAgent.PaneID,
		"-S", start,
	)
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

	// SyncWindowSizes marks windows manually sized; hand sizing back to
	// the client that is about to look at them, or the terminal would be
	// stuck at the dashboard's transcript dimensions.
	r.releaseWindowSizes(ctx)

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
	if err := r.configureStatusBar(ctx, sessionName); err != nil {
		diagnostic.Logger().Warn("tmux status bar unavailable", "error", err)
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

// configureStatusBar dresses the managed session's status bar: the sapphire
// band, the agent's lineage on the left, the return hint on the right, and the
// amber prefix state that replaces the left while the prefix is held. The
// whole thing is versioned by one option so a session configured by an older
// binary is re-dressed rather than left half-current.
func (r *Runtime) configureStatusBar(ctx context.Context, sessionName string) error {
	version, err := r.runner.Run(ctx, nil,
		"show-options", "-qv", "-t", sessionName, statusVersionOption,
	)
	if err != nil {
		return fmt.Errorf("read status bar version: %w", err)
	}
	if version == statusVersion {
		return nil
	}

	options := []struct {
		name  string
		value string
	}{
		{statusStyleBaseOption, baseStatusStyle},
		{statusLeftBaseOption, baseStatusLeft},
		{statusStylePrefixOption, prefixStatusStyle},
		{statusLeftPrefixOption, prefixStatusLeft},
		{statusVersionOption, statusVersion},
		{"status-style", dynamicStatusStyle},
		{"status-left", dynamicStatusLeft},
		{"status-left-length", strconv.Itoa(
			max(baseStatusLeftLength, len(prefixStatusLeft)))},
		{"status-right", r.statusRightHint()},
		{"status-right-length", strconv.Itoa(len(r.statusRightHint()))},
		{"status-format[0]", statusFormat},
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

func (r *Runtime) Send(ctx context.Context, id, message string) error {
	managedAgent, err := r.FindAgent(ctx, id)
	if err != nil {
		return err
	}
	if !managedAgent.ProcessLive {
		return fmt.Errorf("agent %s is not running", id)
	}
	if isSlashCommand(message) {
		// Providers ignore slash commands that arrive as a bracketed paste,
		// so type the command as literal keys instead. -l keeps the text a
		// single un-interpreted argument and -- guards the leading slash.
		if _, err := r.runner.Run(ctx, nil,
			"send-keys", "-t", managedAgent.PaneID, "-l", "--", message,
		); err != nil {
			return err
		}
	} else {
		buffer := "stormlight-" + id
		if _, err := r.runner.Run(ctx, []byte(message), "load-buffer", "-b", buffer, "-"); err != nil {
			return err
		}
		if _, err := r.runner.Run(ctx, nil,
			"paste-buffer", "-d", "-b", buffer, "-t", managedAgent.PaneID,
		); err != nil {
			return err
		}
	}
	// Provider TUIs absorb a paste (or typed command) asynchronously; an
	// Enter arriving in the same instant lands while the composer is still
	// ingesting and gets dropped, leaving the message sitting unsubmitted.
	// A short gap makes the submit reliable.
	time.Sleep(sendSubmitDelay)
	_, err = r.runner.Run(ctx, nil, "send-keys", "-t", managedAgent.PaneID, "Enter")
	if err == nil {
		_ = r.Update(ctx, id, session.Update{Activity: agent.ActivityWorking})
	}
	return err
}

var sendSubmitDelay = 150 * time.Millisecond

// isSlashCommand reports whether a message should reach the provider as a
// typed slash command. Only single-line messages qualify — a slash command
// never contains newlines, so multi-line text keeps the paste path.
func isSlashCommand(message string) bool {
	return strings.HasPrefix(message, "/") && !strings.ContainsAny(message, "\n\r")
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
	if update.TranscriptPath != "" {
		values["@stormlight_transcript_path"] = update.TranscriptPath
	}
	switch {
	case update.ClearAttention:
		values["@stormlight_attention"] = ""
	case update.Attention != "" || update.Activity != "":
		attention := update.Attention
		if attention == agent.AttentionWaiting &&
			managedAgent.Attention.Urgent() && !update.TurnEnded {
			// Waiting is a floor, not a ceiling: a late soft signal must
			// never downgrade an urgent question/approval/auth state. A
			// turn end is different — it proves the prompt that raised the
			// urgency was resolved (in the agent's own terminal), so its
			// classification always applies.
			attention = managedAgent.Attention
		}
		values["@stormlight_attention"] = string(attention)
	}
	switch {
	case update.ClearMark:
		values["@stormlight_mark"] = ""
	case update.Mark != "":
		values["@stormlight_mark"] = string(update.Mark)
	case update.ClearAttention && managedAgent.Mark == agent.MarkAttention:
		// Clearing attention takes the attention mark down with it: both say
		// "this row wants me", and seen is seen.
		values["@stormlight_mark"] = ""
	case (update.Activity != "" || update.Attention != "") &&
		managedAgent.Mark == agent.MarkWorking:
		// An in-progress mark corrects a stale reading of what the agent is
		// doing, and the agent is the authority on that: its next
		// self-report is a fresh reading, so the correction retires. An
		// attention mark is about what the human must do, which no provider
		// event can answer, so it survives every update but an explicit one.
		values["@stormlight_mark"] = ""
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

// SyncWindowSizes resizes every managed agent window to the given size so
// agents render — and are captured — at the width the Spanreed pane
// displays, instead of the 80x24 a detached window defaults to. Sessions
// with an attached client are left alone: the client's terminal governs
// there, and fighting it would resize under the user's feet.
func (r *Runtime) SyncWindowSizes(ctx context.Context, width, height int) error {
	if width < 20 || height < 10 {
		return nil
	}
	clients, err := r.runner.Run(ctx, nil,
		"list-clients", "-t", r.sessionName, "-F", "#{client_name}",
	)
	if err != nil || strings.TrimSpace(clients) != "" {
		// No session yet, or someone is attached — nothing to size.
		return nil
	}
	windows, err := r.runner.Run(ctx, nil,
		"list-windows", "-t", r.sessionName, "-F", "#{window_id}",
	)
	if err != nil {
		return err
	}
	size := fmt.Sprintf("%d", width)
	rows := fmt.Sprintf("%d", height)
	for _, windowID := range strings.Fields(windows) {
		if _, err := r.runner.Run(ctx, nil,
			"resize-window", "-t", windowID, "-x", size, "-y", rows,
		); err != nil {
			return fmt.Errorf("resize window %s: %w", windowID, err)
		}
	}
	return nil
}

// releaseWindowSizes returns every managed window to client-driven sizing
// (window-size latest). Best-effort: a failure only means a window keeps
// the transcript size until the next sync.
func (r *Runtime) releaseWindowSizes(ctx context.Context) {
	windows, err := r.runner.Run(ctx, nil,
		"list-windows", "-t", r.sessionName, "-F", "#{window_id}",
	)
	if err != nil {
		return
	}
	for _, windowID := range strings.Fields(windows) {
		_, _ = r.runner.Run(ctx, nil,
			"set-option", "-w", "-t", windowID, "window-size", "latest",
		)
	}
}

// ApplyServerOptions asserts serverConfig's live-applicable options on a
// running server, so configuration changes shipped in a Stormlight upgrade
// reach appliance servers that never rebooted. A server that is not running
// is left alone: whichever command starts it boots with the config file.
func (r *Runtime) ApplyServerOptions(ctx context.Context) error {
	for _, option := range serverOptions {
		if _, err := r.runner.Run(ctx, nil,
			"set-option", "-g", option[0], option[1],
		); err != nil {
			if strings.Contains(err.Error(), "no server running") ||
				strings.Contains(err.Error(), "failed to connect to server") {
				return nil
			}
			return fmt.Errorf("apply server option %s: %w", option[0], err)
		}
	}
	return nil
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
		ID:             core[0],
		Provider:       agent.Provider(core[1]),
		Name:           parts[3],
		Task:           core[2],
		Summary:        core[3],
		Cwd:            core[4],
		CreatedAt:      createdAt,
		Activity:       activity,
		Attention:      agent.Attention(core[7]),
		TmuxSession:    parts[0],
		WindowID:       parts[1],
		WindowIndex:    windowIndex,
		PaneID:         parts[4],
		Command:        parts[5],
		PaneTitle:      parts[7],
		ProcessLive:    !paneDead,
		ExitCode:       exitCode,
		Mode:           agent.PermissionMode(core[17]),
		TranscriptPath: core[18],
		Mark:           agent.Mark(core[19]),
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
		"@stormlight_id":              managedAgent.ID,
		"@stormlight_provider":        string(managedAgent.Provider),
		"@stormlight_task":            metadataValue(managedAgent.Task),
		"@stormlight_summary":         metadataValue(managedAgent.Summary),
		"@stormlight_cwd":             managedAgent.Cwd,
		"@stormlight_created_at":      createdAt,
		"@stormlight_activity":        string(managedAgent.Activity),
		"@stormlight_attention":       string(managedAgent.Attention),
		"@stormlight_pane":            managedAgent.PaneID,
		"@stormlight_mode":            string(managedAgent.Mode),
		"@stormlight_transcript_path": managedAgent.TranscriptPath,
		"@stormlight_mark":            string(managedAgent.Mark),
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
		"@stormlight_workspace_id":   value.ID,
		"@stormlight_workspace_kind": value.Kind,
		"@stormlight_workspace_name": value.Name,
		"@stormlight_workspace_root": value.Root,
		"@stormlight_execution_root": value.ExecutionRoot,
		"@stormlight_component_name": value.ComponentName,
		"@stormlight_component_root": value.ComponentRoot,
		// The lineage tail is derived, not stored: the status bar needs it as
		// one value because tmux formats cannot pick between a component and
		// a worktree basename. It is written here, alongside the fields it is
		// derived from, so the two can never drift.
		"@stormlight_workspace_tail":     value.Tail(),
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
