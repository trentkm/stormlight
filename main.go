package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/spf13/cobra"
	"github.com/trentkm/stormlight/internal/agent"
	"github.com/trentkm/stormlight/internal/app"
	"github.com/trentkm/stormlight/internal/config"
	"github.com/trentkm/stormlight/internal/diagnostic"
	"github.com/trentkm/stormlight/internal/provider"
	"github.com/trentkm/stormlight/internal/selfpath"
	"github.com/trentkm/stormlight/internal/session"
	"github.com/trentkm/stormlight/internal/surface"
	"github.com/trentkm/stormlight/internal/tmux"
	"github.com/trentkm/stormlight/internal/ui"
	"github.com/trentkm/stormlight/internal/workspace"
)

var version = "dev"

const dashboardHostedEnv = "STORMLIGHT_UI_HOSTED"

func main() {
	root := newRootCommand()
	err := root.Execute()
	if err != nil {
		diagnostic.Logger().Error("command failed", "error", err)
	}
	diagnostic.Close()
	if err != nil {
		os.Exit(1)
	}
}

func newRootCommand() *cobra.Command {
	var socket string
	var sessionName string
	var logFile string
	var logLevel string

	cfg, configWarnings, configErr := config.Load()

	root := &cobra.Command{
		Use:          "stormlight [path]",
		Short:        "A workspace-native control surface for coding agents",
		Args:         cobra.MaximumNArgs(1),
		SilenceUsage: true,
		Version:      version,
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			if cmd.Name() == "logs" {
				return nil
			}
			path, err := diagnostic.Init(logFile, logLevel)
			if err != nil {
				return fmt.Errorf("initialize diagnostics: %w", err)
			}
			diagnostic.Logger().Info("command started",
				"command", cmd.CommandPath(),
				"version", version,
				"log_path", path,
			)
			if configErr != nil {
				diagnostic.Logger().Warn("configuration unavailable; using defaults",
					"error", configErr,
				)
			}
			for _, warning := range configWarnings {
				diagnostic.Logger().Warn("configuration value ignored", "detail", warning)
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			openPath := ""
			if len(args) == 1 {
				resolved, err := openWorkspacePath(args[0])
				if err != nil {
					return err
				}
				openPath = resolved
			}
			if shouldHostDashboard() {
				tmuxPath, lookupErr := exec.LookPath("tmux")
				if lookupErr == nil {
					return hostDashboard(cmd, tmuxPath, socket)
				}
				diagnostic.Logger().Warn(
					"tmux is unavailable; running dashboard directly",
					"error", lookupErr,
				)
			}
			configureHostedDashboard(socket)
			return runDashboard(socket, sessionName, cfg, openPath)
		},
	}
	root.PersistentFlags().StringVar(&socket, "tmux-socket",
		envFirstOr(cfg.SocketOr(tmux.DefaultSocket), "STORMLIGHT_TMUX_SOCKET"),
		"tmux socket name; empty targets the default tmux server",
	)
	root.PersistentFlags().StringVar(&sessionName, "session",
		envFirstOr(configValueOr(cfg.Defaults.Session, "stormlight-agents"),
			"STORMLIGHT_SESSION"),
		"managed tmux session name",
	)
	root.PersistentFlags().StringVar(&logFile, "log-file",
		envFirstOr(cfg.Log.File, "STORMLIGHT_LOG_FILE"),
		"diagnostic log file",
	)
	root.PersistentFlags().StringVar(&logLevel, "log-level",
		envFirstOr(configValueOr(cfg.Log.Level, "info"), "STORMLIGHT_LOG_LEVEL"),
		"diagnostic log level",
	)

	root.AddCommand(
		newDispatchCommand(&socket, &sessionName, cfg),
		newListCommand(&socket, &sessionName, cfg),
		newAttachCommand(&socket, &sessionName, cfg),
		newSendCommand(&socket, &sessionName, cfg),
		newRenameCommand(&socket, &sessionName, cfg),
		newStopCommand(&socket, &sessionName, cfg),
		newDeleteCommand(&socket, &sessionName, cfg),
		newMarkCommand(&socket, &sessionName, cfg),
		newNextCommand(&socket, &sessionName, cfg),
		newEventCommand(&socket, &sessionName, cfg),
		newProviderEventCommand(&socket, &sessionName, cfg),
		newLogsCommand(&logFile),
		newRunCommand(&socket, &sessionName, cfg),
		newConfigCommand(&socket, &sessionName, cfg),
	)
	return root
}

func configValueOr(value, fallback string) string {
	if value != "" {
		return value
	}
	return fallback
}

func providerSpecs(cfg config.Config) []provider.Spec {
	names := make([]string, 0, len(cfg.Providers))
	for name := range cfg.Providers {
		names = append(names, name)
	}
	slices.Sort(names)
	specs := make([]provider.Spec, 0, len(names))
	for _, name := range names {
		entry := cfg.Providers[name]
		modeArgs := make(map[agent.PermissionMode][]string, len(entry.ModeArgs))
		for mode, args := range entry.ModeArgs {
			parsed, err := agent.ParseMode(mode)
			if err != nil {
				continue
			}
			modeArgs[parsed] = args
		}
		specs = append(specs, provider.Spec{
			ID:        agent.Provider(name),
			Label:     entry.Label,
			Binary:    entry.Binary,
			Args:      entry.Args,
			ExtraArgs: entry.ExtraArgs,
			ModeArgs:  modeArgs,
		})
	}
	return specs
}

func newConfigCommand(socket, sessionName *string, cfg config.Config) *cobra.Command {
	command := &cobra.Command{
		Use:   "config",
		Short: "Show the effective configuration",
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Fprintln(cmd.OutOrStdout(), "# "+config.Path())
			rendered, err := cfg.EffectiveTOML(*socket, *sessionName)
			if err != nil {
				return err
			}
			fmt.Fprint(cmd.OutOrStdout(), rendered)
			return nil
		},
	}
	var force bool
	initCommand := &cobra.Command{
		Use:   "init",
		Short: "Write a commented configuration template",
		RunE: func(cmd *cobra.Command, args []string) error {
			path, err := config.WriteTemplate(force)
			if err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), path)
			return nil
		},
	}
	initCommand.Flags().BoolVar(&force, "force", false, "overwrite an existing config file")
	command.AddCommand(initCommand)
	return command
}

// openWorkspacePath validates the optional root-command argument early, so
// a bad path fails in the caller's terminal instead of inside the hosted
// dashboard session.
func openWorkspacePath(argument string) (string, error) {
	absolute, err := filepath.Abs(argument)
	if err != nil {
		return "", fmt.Errorf("resolve workspace path: %w", err)
	}
	info, err := os.Stat(absolute)
	if err != nil || !info.IsDir() {
		return "", fmt.Errorf("not a directory: %s", argument)
	}
	return absolute, nil
}

func shouldHostDashboard() bool {
	return os.Getenv("TMUX") == "" && !dashboardIsHosted()
}

func dashboardIsHosted() bool {
	return os.Getenv(dashboardHostedEnv) != ""
}

func runDashboard(socket, sessionName string, cfg config.Config, openPath string) error {
	service, err := newService(socket, sessionName, cfg)
	if err != nil {
		return err
	}
	// A running appliance server keeps the options it booted with, so a
	// dashboard launch is the moment upgraded configuration reaches it. The
	// user's default server (empty socket) is never touched.
	if runtime, ok := service.Runtime().(*tmux.Runtime); ok && socket != "" {
		if err := runtime.ApplyServerOptions(context.Background()); err != nil {
			diagnostic.Logger().Warn("apply server options failed", "error", err)
		}
	}
	options := ui.Options{
		YaziPath:        cfg.Tools.Yazi,
		NvimPath:        cfg.Tools.Nvim,
		DefaultProvider: agent.Provider(cfg.Defaults.Provider),
		ExpandedRows:    cfg.UI.Rows == "expanded",
		ModeForDir:      cfg.ModeForDir,
		ProviderForDir:  cfg.ProviderForDir,
		Columns:         ui.LoadColumnPrefs(),
	}
	if openPath != "" {
		value, err := service.AddWorkspace(context.Background(), openPath)
		if err != nil {
			return fmt.Errorf("open workspace %s: %w", openPath, err)
		}
		options.SelectWorkspaceID = value.ID
	}
	currentSurface := surface.Surface(surface.NewDirect())
	if os.Getenv("TMUX") != "" {
		if tmuxPath, lookupErr := exec.LookPath("tmux"); lookupErr == nil {
			currentSurface = tmux.NewSurface(tmuxPath)
		}
	}
	options.DefaultMode, err = agent.ParseMode(cfg.Defaults.Mode)
	if err != nil {
		options.DefaultMode = agent.DefaultMode
	}
	// The alt screen and mouse reporting are declared by the dashboard's
	// View rather than requested here; see ui.Model.View.
	//
	// There is no input filter any more. v1 split fast wheel bursts across
	// reads and delivered the leftovers as literal key runes, so a hook had
	// to recognise and discard them; v2's parser holds an incomplete escape
	// sequence until the rest arrives, which was confirmed by feeding it one
	// SGR report cut at three different points and getting a single clean
	// wheel event and no stray keys each time.
	program := tea.NewProgram(
		ui.NewModelWithOptions(service, currentSurface, options),
	)
	_, err = program.Run()
	return err
}

func hostDashboard(command *cobra.Command, tmuxPath, socket string) error {
	executable, err := selfpath.Resolve()
	if err != nil {
		return err
	}
	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("find dashboard directory: %w", err)
	}

	session := dashboardSessionName(os.Getpid(), time.Now())
	shellCommand := dashboardShellCommand(executable, os.Args[1:])
	args := dashboardHostArgs(
		socket,
		stormlightServerConfig(socket),
		session,
		cwd,
		shellCommand,
	)
	process := exec.Command(tmuxPath, args...)
	process.Stdin = command.InOrStdin()
	process.Stdout = command.OutOrStdout()
	process.Stderr = command.ErrOrStderr()

	defer cleanupDashboardSession(tmuxPath, socket, session)
	if err := process.Run(); err != nil {
		return fmt.Errorf("host dashboard in tmux: %w", err)
	}
	printFarewell(command.OutOrStdout())
	return nil
}

// printFarewell replaces tmux's parting "[exited]" line with Stormlight's
// own. Only on a clean exit — an error's output must stay readable — and
// only on a real terminal.
func printFarewell(out io.Writer) {
	file, ok := out.(*os.File)
	if !ok {
		return
	}
	info, err := file.Stat()
	if err != nil || info.Mode()&os.ModeCharDevice == 0 {
		return
	}
	// Cursor up onto tmux's "[exited]" line, clear it, speak the oath.
	fmt.Fprint(file, "\x1b[A\x1b[2K")
	fmt.Fprintln(file,
		"\x1b[38;2;125;207;255m✦\x1b[0m \x1b[2mJourney before destination.\x1b[0m")
}

func dashboardSessionName(pid int, now time.Time) string {
	return fmt.Sprintf(
		"stormlight-ui-%d-%06x",
		pid,
		uint64(now.UnixNano())&0xffffff,
	)
}

func dashboardShellCommand(executable string, args []string) string {
	command := append([]string{executable}, args...)
	return dashboardHostedEnv + "=1 exec " + shellJoinCommand(command)
}

func dashboardHostArgs(socket, config, session, cwd, shellCommand string) []string {
	args := make([]string, 0, 12)
	if socket != "" {
		args = append(args, "-L", socket)
	}
	if config != "" {
		args = append(args, "-f", config)
	}
	return append(
		args,
		"new-session",
		"-s", session,
		"-c", cwd,
		"-n", "stormlight",
		shellCommand,
	)
}

func configureHostedDashboard(socket string) {
	if !dashboardIsHosted() || os.Getenv("TMUX") == "" {
		return
	}
	tmuxPath, err := exec.LookPath("tmux")
	if err != nil {
		diagnostic.Logger().Warn(
			"cannot configure hosted dashboard",
			"error", err,
		)
		return
	}

	pane := os.Getenv("TMUX_PANE")
	displayArgs := tmuxSocketArgs(socket,
		"display-message", "-p", "-t", pane, "#{session_id}",
	)
	output, err := exec.Command(tmuxPath, displayArgs...).CombinedOutput()
	if err != nil {
		diagnostic.Logger().Warn(
			"cannot identify hosted dashboard session",
			"error", strings.TrimSpace(string(output)),
		)
		return
	}
	session := strings.TrimSpace(string(output))
	args := tmuxSocketArgs(
		socket,
		"set-option", "-t", session, "status", "off",
	)
	if output, err := exec.Command(tmuxPath, args...).CombinedOutput(); err != nil {
		diagnostic.Logger().Warn(
			"cannot hide hosted dashboard status",
			"error", strings.TrimSpace(string(output)),
		)
	}
}

func cleanupDashboardSession(tmuxPath, socket, session string) {
	args := tmuxSocketArgs(socket, "kill-session", "-t", session)
	_ = exec.Command(tmuxPath, args...).Run()
}

func tmuxSocketArgs(socket string, args ...string) []string {
	if socket == "" {
		return args
	}
	return append([]string{"-L", socket}, args...)
}

func shellJoinCommand(args []string) string {
	quoted := make([]string, len(args))
	for index, arg := range args {
		if arg == "" {
			quoted[index] = "''"
			continue
		}
		quoted[index] = "'" + strings.ReplaceAll(arg, "'", "'\"'\"'") + "'"
	}
	return strings.Join(quoted, " ")
}

func newLogsCommand(logFile *string) *cobra.Command {
	var lines int
	var showPath bool
	command := &cobra.Command{
		Use:   "logs",
		Short: "Show recent diagnostic log entries",
		RunE: func(cmd *cobra.Command, args []string) error {
			path, err := diagnostic.ResolvePath(*logFile)
			if err != nil {
				return err
			}
			if showPath {
				fmt.Fprintln(cmd.OutOrStdout(), path)
				return nil
			}
			tail, err := diagnostic.Tail(path, lines)
			if err != nil {
				return err
			}
			if tail != "" {
				fmt.Fprintln(cmd.OutOrStdout(), tail)
			}
			return nil
		},
	}
	command.Flags().IntVarP(&lines, "lines", "n", 100, "number of recent entries")
	command.Flags().BoolVar(&showPath, "path", false, "print the log file path")
	return command
}

func newService(socket, sessionName string, cfg config.Config) (*app.Service, error) {
	client := tmux.NewClient(socket)
	if serverConfig := stormlightServerConfig(socket); serverConfig != "" {
		client = tmux.NewClientWithConfig(socket, serverConfig)
	}
	runtime, err := tmux.NewRuntime(client, sessionName)
	if err != nil {
		return nil, err
	}
	if len(cfg.Tmux.ReturnKeys) > 0 {
		runtime.SetReturnKeys(cfg.Tmux.ReturnKeys)
	}
	if len(cfg.Tmux.NextKeys) > 0 {
		runtime.SetNextKeys(cfg.Tmux.NextKeys)
	}
	if len(cfg.Tmux.PreviousKeys) > 0 {
		runtime.SetPreviousKeys(cfg.Tmux.PreviousKeys)
	}
	registry := provider.NewRegistryWithSpecs(providerSpecs(cfg))
	return app.NewService(runtime, registry, workspace.NewRegistry()), nil
}

// stormlightServerConfig resolves the config for Stormlight-owned tmux
// servers. An empty socket targets the user's default server, which keeps
// their own configuration; Stormlight's config is never applied there.
func stormlightServerConfig(socket string) string {
	if socket == "" {
		return ""
	}
	path, err := tmux.EnsureServerConfig(config.Dir())
	if err != nil {
		diagnostic.Logger().Warn(
			"cannot prepare Stormlight tmux config; using tmux defaults",
			"error", err,
		)
		return ""
	}
	return path
}

func newDispatchCommand(socket, sessionName *string, cfg config.Config) *cobra.Command {
	var providerName string
	var cwd string
	var name string
	var modeName string

	command := &cobra.Command{
		Use:   "dispatch [task]",
		Short: "Start an agent in a managed tmux window",
		Args:  cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			task, err := readTask(args)
			if err != nil {
				return err
			}
			mode, err := agent.ParseMode(modeName)
			if err != nil {
				return err
			}
			if overrideDir, dirErr := dispatchDirectory(cwd); dirErr == nil {
				if !cmd.Flags().Changed("mode") {
					if override, ok := cfg.ModeForDir(overrideDir); ok {
						mode = override
					}
				}
				if !cmd.Flags().Changed("provider") {
					if override, ok := cfg.ProviderForDir(overrideDir); ok {
						providerName = string(override)
					}
				}
			}
			service, err := newService(*socket, *sessionName, cfg)
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 15*time.Second)
			defer cancel()
			managedAgent, err := service.Dispatch(ctx, app.DispatchRequest{
				Provider: agent.Provider(providerName),
				Name:     name,
				Task:     task,
				Cwd:      cwd,
				Mode:     mode,
			})
			if err != nil {
				return err
			}
			fmt.Printf("%s\t%s\t%s\n", managedAgent.ID, managedAgent.Name, managedAgent.WindowID)
			return nil
		},
	}
	command.Flags().StringVarP(&providerName, "provider", "p",
		configValueOr(cfg.Defaults.Provider, string(agent.ProviderCodex)),
		"provider: codex, claude, or a configured provider")
	command.Flags().StringVarP(&cwd, "cwd", "C", "", "working directory")
	command.Flags().StringVarP(&name, "name", "n", "", "tmux window name")
	command.Flags().StringVarP(&modeName, "mode", "m",
		configValueOr(cfg.Defaults.Mode, string(agent.DefaultMode)),
		"permission mode: ask, edits, or auto")
	return command
}

// dispatchDirectory resolves the directory a dispatch will run in, for
// matching per-workspace config overrides.
func dispatchDirectory(cwd string) (string, error) {
	if strings.TrimSpace(cwd) == "" {
		return os.Getwd()
	}
	return filepath.Abs(cwd)
}

func newListCommand(socket, sessionName *string, cfg config.Config) *cobra.Command {
	var asJSON bool
	command := &cobra.Command{
		Use:   "list",
		Short: "List managed agents",
		RunE: func(cmd *cobra.Command, args []string) error {
			service, err := newService(*socket, *sessionName, cfg)
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 5*time.Second)
			defer cancel()
			agents, err := service.ListAgents(ctx)
			if err != nil {
				return err
			}
			if asJSON {
				encoder := json.NewEncoder(os.Stdout)
				encoder.SetIndent("", "  ")
				return encoder.Encode(agents)
			}
			if len(agents) == 0 {
				fmt.Println("No managed agents.")
				return nil
			}
			fmt.Printf("%-10s %-8s %-11s %-24s %s\n", "ID", "PROVIDER", "STATE", "NAME", "TASK")
			for _, managedAgent := range agents {
				fmt.Printf("%-10s %-8s %-11s %-24s %s\n",
					shortID(managedAgent.ID),
					managedAgent.Provider,
					managedAgent.Activity,
					truncatePlain(managedAgent.Name, 24),
					truncatePlain(managedAgent.DisplaySummary(), 60),
				)
			}
			return nil
		},
	}
	command.Flags().BoolVar(&asJSON, "json", false, "emit JSON")
	return command
}

func newAttachCommand(socket, sessionName *string, cfg config.Config) *cobra.Command {
	return &cobra.Command{
		Use:   "attach <id>",
		Short: "Switch the current tmux client to an agent",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			service, err := newService(*socket, *sessionName, cfg)
			if err != nil {
				return err
			}
			result, err := service.Attach(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			if result.Command == nil {
				return nil
			}
			result.Command.Stdin = cmd.InOrStdin()
			result.Command.Stdout = cmd.OutOrStdout()
			result.Command.Stderr = cmd.ErrOrStderr()
			if err := result.Command.Run(); err != nil {
				return fmt.Errorf("attach tmux session: %w", err)
			}
			return nil
		},
	}
}

func newRenameCommand(socket, sessionName *string, cfg config.Config) *cobra.Command {
	return &cobra.Command{
		Use:   "rename <id> <name>",
		Short: "Rename an agent",
		Args:  cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			service, err := newService(*socket, *sessionName, cfg)
			if err != nil {
				return err
			}
			return service.Rename(cmd.Context(), args[0], strings.Join(args[1:], " "))
		},
	}
}

func newSendCommand(socket, sessionName *string, cfg config.Config) *cobra.Command {
	return &cobra.Command{
		Use:   "send <id> <message>",
		Short: "Send a message to an agent",
		Args:  cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			service, err := newService(*socket, *sessionName, cfg)
			if err != nil {
				return err
			}
			return service.Send(cmd.Context(), args[0], strings.Join(args[1:], " "))
		},
	}
}

func newStopCommand(socket, sessionName *string, cfg config.Config) *cobra.Command {
	return &cobra.Command{
		Use:   "stop <id>",
		Short: "Interrupt an agent",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			service, err := newService(*socket, *sessionName, cfg)
			if err != nil {
				return err
			}
			return service.Interrupt(cmd.Context(), args[0])
		},
	}
}

func newDeleteCommand(socket, sessionName *string, cfg config.Config) *cobra.Command {
	return &cobra.Command{
		Use:   "delete <id>",
		Short: "Delete an agent tmux window",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			service, err := newService(*socket, *sessionName, cfg)
			if err != nil {
				return err
			}
			return service.Delete(cmd.Context(), args[0])
		},
	}
}

func newMarkCommand(socket, sessionName *string, cfg config.Config) *cobra.Command {
	return &cobra.Command{
		Use:   "mark <id> <working|attention|none>",
		Short: "Record your own reading of an agent's state",
		Long: "Override what Stormlight infers about an agent: working says " +
			"it is still going, attention flags it to come back to, and none " +
			"hands the row back to Stormlight's own reading.",
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			mark, err := agent.ParseMark(args[1])
			if err != nil {
				return err
			}
			service, err := newService(*socket, *sessionName, cfg)
			if err != nil {
				return err
			}
			return service.SetMark(cmd.Context(), args[0], mark)
		},
	}
}

// newNextCommand is what the tmux queue keys invoke. The ordering it applies
// depends on marks and on when each agent started waiting — inference tmux
// formats cannot rank — so the binding calls back into the binary that keeps
// that record rather than trying to express it as a format.
func newNextCommand(socket, sessionName *string, cfg config.Config) *cobra.Command {
	var client string
	var window string
	var agentID string
	var previous bool

	command := &cobra.Command{
		Use:   "next",
		Short: "Switch to the next agent waiting on you",
		Long: "Hand the terminal the next agent in the attention queue, " +
			"oldest first. From inside an agent the queue steps past it; " +
			"from anywhere else it starts at the end it came from. Visiting " +
			"an agent leaves its attention exactly as it was.",
		RunE: func(cmd *cobra.Command, args []string) error {
			service, err := newService(*socket, *sessionName, cfg)
			if err != nil {
				return err
			}
			runtime, ok := service.Runtime().(*tmux.Runtime)
			if !ok {
				return fmt.Errorf("the queue needs a tmux runtime")
			}
			step := agent.QueueForward
			if previous {
				step = agent.QueueBack
			}
			return runtime.NextWaiting(cmd.Context(), tmux.NextRequest{
				Client: client,
				Window: window,
				Agent:  agentID,
				Step:   step,
			})
		},
	}
	command.Flags().StringVar(&client, "client", "",
		"tmux client to switch (default: the current one)")
	command.Flags().StringVar(&window, "window", "",
		"tmux window the request came from")
	command.Flags().StringVar(&agentID, "agent", "",
		"agent the request came from; the queue steps past it")
	command.Flags().BoolVar(&previous, "previous", false,
		"step back through the queue instead of forward")
	return command
}

func newEventCommand(socket, sessionName *string, cfg config.Config) *cobra.Command {
	var id string
	var state string
	var attention string
	var summary string

	command := &cobra.Command{
		Use:   "event",
		Short: "Update agent state from a provider hook",
		RunE: func(cmd *cobra.Command, args []string) error {
			if id == "" {
				id = agentIDFromEnv()
			}
			if id == "" {
				return fmt.Errorf("agent id is required; pass --id or run inside a Stormlight agent")
			}
			activity, err := parseActivity(state)
			if err != nil {
				return err
			}
			attentionKind, err := parseAttention(attention)
			if err != nil {
				return err
			}
			service, err := newService(*socket, *sessionName, cfg)
			if err != nil {
				return err
			}
			return service.Update(cmd.Context(), id, session.Update{
				Activity:  activity,
				Attention: attentionKind,
				Summary:   summary,
			})
		},
	}
	command.Flags().StringVar(&id, "id", "", "agent id")
	command.Flags().StringVar(&state, "state", "", "starting, working, idle, completed, failed, or stopped")
	command.Flags().StringVar(&attention, "attention", "", "none, question, approval, auth, or waiting")
	command.Flags().StringVar(&summary, "summary", "", "one-line status summary")
	return command
}

func newProviderEventCommand(socket, sessionName *string, cfg config.Config) *cobra.Command {
	// Every failure here is logged and swallowed. A hook that exits
	// non-zero is the provider's problem to report, and no dashboard
	// bookkeeping is worth interrupting an agent mid-turn for — but a hook
	// that quietly does nothing is exactly what makes broken lifecycle
	// wiring so hard to spot, so the log always says what happened.
	return &cobra.Command{
		Use:    "_provider-event <provider> [payload]",
		Hidden: true,
		// Hooks deliver the payload on stdin; Codex's `notify` callback
		// passes it as an argument instead.
		Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			providerID := agent.Provider(args[0])
			id := agentIDFromEnv()
			if id == "" {
				diagnostic.Logger().Warn("provider event outside a managed agent",
					"provider", providerID,
				)
				return nil
			}

			var payload []byte
			if len(args) == 2 {
				payload = []byte(args[1])
			} else {
				read, err := io.ReadAll(cmd.InOrStdin())
				if err != nil {
					diagnostic.Logger().Warn("read provider event payload",
						"provider", providerID,
						"error", err,
					)
					return nil
				}
				payload = read
			}
			event, handled, err := provider.ParseEvent(providerID, payload)
			if err != nil {
				diagnostic.Logger().Warn("parse provider event",
					"provider", providerID,
					"error", err,
				)
				return nil
			}
			if !handled {
				diagnostic.Logger().Debug("provider event ignored",
					"provider", providerID,
					"agent", id,
				)
				return nil
			}
			diagnostic.Logger().Debug("provider event",
				"provider", providerID,
				"agent", id,
				"activity", event.Activity,
				"attention", event.Attention,
			)
			service, err := newService(*socket, *sessionName, cfg)
			if err != nil {
				diagnostic.Logger().Warn("provider event service unavailable",
					"provider", providerID,
					"error", err,
				)
				return nil
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 5*time.Second)
			defer cancel()
			if err := service.Update(ctx, id, session.Update{
				Activity:       event.Activity,
				Attention:      event.Attention,
				Summary:        event.Summary,
				TranscriptPath: event.TranscriptPath,
				TurnEnded:      event.TurnEnded,
			}); err != nil {
				diagnostic.Logger().Warn("apply provider event",
					"provider", providerID,
					"agent", id,
					"error", err,
				)
			}
			return nil
		},
	}
}

func newRunCommand(socket, sessionName *string, cfg config.Config) *cobra.Command {
	var id string
	var window string
	var cwd string
	var encodedLaunch string

	command := &cobra.Command{
		Use:    "_run",
		Hidden: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if id == "" || window == "" {
				return fmt.Errorf("internal agent identity is missing")
			}
			launch, err := tmux.DecodeLaunch(encodedLaunch)
			if err != nil {
				return err
			}
			service, err := newService(*socket, *sessionName, cfg)
			if err != nil {
				return err
			}
			updateCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			_ = service.Update(updateCtx, id, session.Update{Activity: agent.ActivityWorking})
			cancel()

			// Hooks resolve $STORMLIGHT_BIN long after this process
			// started, so it has to name a binary that survives an
			// upgrade rather than however this invocation was spelled.
			stormlightBin, err := selfpath.Resolve()
			if err != nil {
				return err
			}

			child := exec.Command(launch.Path, launch.Args...)
			child.Dir = cwd
			child.Stdin = os.Stdin
			child.Stdout = os.Stdout
			child.Stderr = os.Stderr
			child.Env = append(os.Environ(),
				"STORMLIGHT_ID="+id,
				"STORMLIGHT_WINDOW="+window,
				"STORMLIGHT_BIN="+stormlightBin,
				"STORMLIGHT_SESSION="+*sessionName,
				"STORMLIGHT_TMUX_SOCKET="+*socket,
			)

			runErr := child.Run()
			activity := agent.ActivityCompleted
			if runErr != nil {
				activity = agent.ActivityFailed
				var exitErr *exec.ExitError
				if errors.As(runErr, &exitErr) {
					code := exitErr.ExitCode()
					if code == 130 || code == 143 {
						activity = agent.ActivityStopped
					}
				}
			}

			updateCtx, cancel = context.WithTimeout(context.Background(), 5*time.Second)
			_ = service.Update(updateCtx, id, session.Update{Activity: activity})
			cancel()
			if runErr != nil {
				return fmt.Errorf("%s exited: %w", launch.Path, runErr)
			}
			return nil
		},
	}
	command.Flags().StringVar(&id, "id", "", "agent id")
	command.Flags().StringVar(&window, "window", "", "tmux window id")
	command.Flags().StringVar(&cwd, "cwd", "", "working directory")
	command.Flags().StringVar(&encodedLaunch, "launch", "", "encoded launch payload")
	return command
}

func readTask(args []string) (string, error) {
	if len(args) > 0 {
		return strings.TrimSpace(strings.Join(args, " ")), nil
	}
	info, err := os.Stdin.Stat()
	if err != nil {
		return "", err
	}
	if info.Mode()&os.ModeCharDevice != 0 {
		return "", fmt.Errorf("task is required")
	}
	data, err := io.ReadAll(os.Stdin)
	if err != nil {
		return "", err
	}
	task := strings.TrimSpace(string(data))
	if task == "" {
		return "", fmt.Errorf("task is required")
	}
	return task, nil
}

func parseActivity(value string) (agent.Activity, error) {
	activity := agent.Activity(value)
	switch activity {
	case "", agent.ActivityStarting, agent.ActivityWorking, agent.ActivityIdle,
		agent.ActivityCompleted, agent.ActivityFailed, agent.ActivityStopped:
		return activity, nil
	default:
		return "", fmt.Errorf("invalid state %q", value)
	}
}

func parseAttention(value string) (agent.Attention, error) {
	if value == "none" {
		return agent.AttentionNone, nil
	}
	attention := agent.Attention(value)
	switch attention {
	case "", agent.AttentionQuestion, agent.AttentionApproval,
		agent.AttentionAuth, agent.AttentionWaiting:
		return attention, nil
	default:
		return "", fmt.Errorf("invalid attention type %q", value)
	}
}

func shortID(id string) string {
	if len(id) <= 8 {
		return id
	}
	return id[:8]
}

func truncatePlain(value string, length int) string {
	value = strings.Join(strings.Fields(value), " ")
	runes := []rune(value)
	if len(runes) <= length {
		return value
	}
	if length <= 1 {
		return string(runes[:length])
	}
	return string(runes[:length-1]) + "…"
}

func envFirst(names ...string) string {
	for _, name := range names {
		if value := os.Getenv(name); value != "" {
			return value
		}
	}
	return ""
}

func envFirstOr(fallback string, names ...string) string {
	if value := envFirst(names...); value != "" {
		return value
	}
	return fallback
}

func agentIDFromEnv() string {
	return os.Getenv("STORMLIGHT_ID")
}
