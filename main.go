package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
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
	"github.com/trentkm/stormlight/internal/session"
	"github.com/trentkm/stormlight/internal/surface"
	"github.com/trentkm/stormlight/internal/ui"
	"github.com/trentkm/stormlight/internal/windrun"
	"github.com/trentkm/stormlight/internal/workspace"
	"github.com/trentkm/windrunner"
	wrclient "github.com/trentkm/windrunner/client"
	"github.com/trentkm/windrunner/server"
)

var version = "dev"

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
			return runDashboard(cmd, cfg, openPath)
		},
	}
	root.PersistentFlags().StringVar(&logFile, "log-file",
		envFirstOr(cfg.Log.File, "STORMLIGHT_LOG_FILE"),
		"diagnostic log file",
	)
	root.PersistentFlags().StringVar(&logLevel, "log-level",
		envFirstOr(configValueOr(cfg.Log.Level, "info"), "STORMLIGHT_LOG_LEVEL"),
		"diagnostic log level",
	)

	root.AddCommand(
		newDispatchCommand(cfg),
		newListCommand(cfg),
		newAttachCommand(cfg),
		newSendCommand(cfg),
		newRenameCommand(cfg),
		newStopCommand(cfg),
		newDeleteCommand(cfg),
		newMarkCommand(cfg),
		newEventCommand(cfg),
		newProviderEventCommand(cfg),
		newLogsCommand(&logFile),
		newConfigCommand(cfg),
		newWindrunnerDaemonCommand(),
		newWindrunnerAttachCommand(),
	)
	return root
}

// newWindrunnerDaemonCommand hosts the windrunner engine: agents' PTYs and
// terminal state live here, outliving every dashboard. Started on demand
// by the windrun runtime; running it by hand is only for debugging.
func newWindrunnerDaemonCommand() *cobra.Command {
	return &cobra.Command{
		Use:    "_wrdaemon",
		Hidden: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			path := windrun.SocketPath()
			os.Remove(path)
			listener, err := net.Listen("unix", path)
			if err != nil {
				return err
			}
			defer os.Remove(path)
			engine := windrunner.NewEngine()
			defer engine.Close()
			diagnostic.Logger().Info("windrunner daemon serving", "socket", path)
			return server.Serve(engine, listener)
		},
	}
}

// newWindrunnerAttachCommand is the F key's other half: a full-terminal
// interactive attachment, ctrl+q to come back. The dashboard runs it
// through ExecProcess.
func newWindrunnerAttachCommand() *cobra.Command {
	return &cobra.Command{
		Use:    "_wrattach <session-id>",
		Hidden: true,
		Args:   cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := wrclient.Dial(windrun.SocketPath())
			if err != nil {
				return err
			}
			defer c.Close()
			result, err := c.Interactive(args[0], 0x11) // ctrl+q
			if err != nil {
				return err
			}
			switch result {
			case wrclient.SessionExited:
				fmt.Fprintln(os.Stderr, "\r\n[agent process exited]")
			case wrclient.ConnectionLost:
				fmt.Fprintln(os.Stderr, "\r\n[windrunner daemon connection lost]")
			}
			return nil
		},
	}
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

func newConfigCommand(cfg config.Config) *cobra.Command {
	command := &cobra.Command{
		Use:   "config",
		Short: "Show the effective configuration",
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Fprintln(cmd.OutOrStdout(), "# "+config.Path())
			rendered, err := cfg.EffectiveTOML()
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
// a bad path fails before the dashboard takes the terminal.
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

func runDashboard(command *cobra.Command, cfg config.Config, openPath string) error {
	service, err := newService(cfg)
	if err != nil {
		return err
	}
	// The session history log accretes one line per provider event, and
	// dashboard launch is the natural moment to fold it down: off every
	// event path, once per run. Best-effort — a log that cannot compact
	// still appends and reads.
	go func() {
		if err := service.CompactSessionHistory(); err != nil {
			diagnostic.Logger().Warn("session history compaction failed", "error", err)
		}
	}()
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
		ui.NewModelWithOptions(service, surface.NewDirect(), options),
	)
	if _, err = program.Run(); err != nil {
		return err
	}
	printFarewell(command.OutOrStdout())
	return nil
}

// printFarewell speaks the oath on the way out. Only on a clean exit — an
// error's output must stay readable — and only on a real terminal.
func printFarewell(out io.Writer) {
	file, ok := out.(*os.File)
	if !ok {
		return
	}
	info, err := file.Stat()
	if err != nil || info.Mode()&os.ModeCharDevice == 0 {
		return
	}
	fmt.Fprintln(file,
		"\x1b[38;2;125;207;255m✦\x1b[0m \x1b[2mJourney before destination.\x1b[0m")
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

// newService builds the app service over the windrunner runtime: agents
// live in the windrunner daemon — PTYs the engine owns, terminals that are
// authoritative state rather than sampled panes — so they outlive every
// dashboard and every terminal the dashboard ran in.
func newService(cfg config.Config) (*app.Service, error) {
	runtime, err := windrun.NewRuntime()
	if err != nil {
		return nil, err
	}
	registry := provider.NewRegistryWithSpecs(providerSpecs(cfg))
	return app.NewService(runtime, registry, workspace.NewRegistry()), nil
}

func newDispatchCommand(cfg config.Config) *cobra.Command {
	var providerName string
	var cwd string
	var name string
	var modeName string

	command := &cobra.Command{
		Use:   "dispatch [task]",
		Short: "Start a managed agent",
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
			service, err := newService(cfg)
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
			fmt.Printf("%s\t%s\n", managedAgent.ID, managedAgent.Name)
			return nil
		},
	}
	command.Flags().StringVarP(&providerName, "provider", "p",
		configValueOr(cfg.Defaults.Provider, string(agent.ProviderCodex)),
		"provider: codex, claude, or a configured provider")
	command.Flags().StringVarP(&cwd, "cwd", "C", "", "working directory")
	command.Flags().StringVarP(&name, "name", "n", "", "agent name")
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

func newListCommand(cfg config.Config) *cobra.Command {
	var asJSON bool
	command := &cobra.Command{
		Use:   "list",
		Short: "List managed agents",
		RunE: func(cmd *cobra.Command, args []string) error {
			service, err := newService(cfg)
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

func newAttachCommand(cfg config.Config) *cobra.Command {
	return &cobra.Command{
		Use:   "attach <id>",
		Short: "Attach an agent's terminal, full screen",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			service, err := newService(cfg)
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
				return fmt.Errorf("attach agent: %w", err)
			}
			return nil
		},
	}
}

func newRenameCommand(cfg config.Config) *cobra.Command {
	return &cobra.Command{
		Use:   "rename <id> <name>",
		Short: "Rename an agent",
		Args:  cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			service, err := newService(cfg)
			if err != nil {
				return err
			}
			return service.Rename(cmd.Context(), args[0], strings.Join(args[1:], " "))
		},
	}
}

func newSendCommand(cfg config.Config) *cobra.Command {
	return &cobra.Command{
		Use:   "send <id> <message>",
		Short: "Send a message to an agent",
		Args:  cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			service, err := newService(cfg)
			if err != nil {
				return err
			}
			return service.Send(cmd.Context(), args[0], strings.Join(args[1:], " "))
		},
	}
}

func newStopCommand(cfg config.Config) *cobra.Command {
	return &cobra.Command{
		Use:   "stop <id>",
		Short: "Interrupt an agent",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			service, err := newService(cfg)
			if err != nil {
				return err
			}
			return service.Interrupt(cmd.Context(), args[0])
		},
	}
}

func newDeleteCommand(cfg config.Config) *cobra.Command {
	return &cobra.Command{
		Use:   "delete <id>",
		Short: "Delete an agent",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			service, err := newService(cfg)
			if err != nil {
				return err
			}
			return service.Delete(cmd.Context(), args[0])
		},
	}
}

func newMarkCommand(cfg config.Config) *cobra.Command {
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
			service, err := newService(cfg)
			if err != nil {
				return err
			}
			return service.SetMark(cmd.Context(), args[0], mark)
		},
	}
}

func newEventCommand(cfg config.Config) *cobra.Command {
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
			service, err := newService(cfg)
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

func newProviderEventCommand(cfg config.Config) *cobra.Command {
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
			service, err := newService(cfg)
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
				SessionID:      event.SessionID,
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
