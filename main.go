package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/spf13/cobra"
	"github.com/trentkm/runstead/internal/agent"
	"github.com/trentkm/runstead/internal/app"
	"github.com/trentkm/runstead/internal/diagnostic"
	"github.com/trentkm/runstead/internal/pending"
	"github.com/trentkm/runstead/internal/provider"
	"github.com/trentkm/runstead/internal/tmux"
	"github.com/trentkm/runstead/internal/ui"
	"github.com/trentkm/runstead/internal/workspace"
)

var version = "dev"

const dashboardHostedEnv = "RUNSTEAD_UI_HOSTED"

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

	root := &cobra.Command{
		Use:          "runstead",
		Short:        "A workspace-native control surface for coding agents",
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
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
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
			return runDashboard(socket, sessionName)
		},
	}
	root.PersistentFlags().StringVar(&socket, "tmux-socket",
		envFirst("RUNSTEAD_TMUX_SOCKET", "AGENTMUX_TMUX_SOCKET"),
		"tmux socket name",
	)
	root.PersistentFlags().StringVar(&sessionName, "session",
		envFirstOr("runstead-agents", "RUNSTEAD_SESSION", "AGENTMUX_SESSION"),
		"managed tmux session name",
	)
	root.PersistentFlags().StringVar(&logFile, "log-file",
		envFirst("RUNSTEAD_LOG_FILE", "AGENTMUX_LOG_FILE"),
		"diagnostic log file",
	)
	root.PersistentFlags().StringVar(&logLevel, "log-level",
		envFirstOr("info", "RUNSTEAD_LOG_LEVEL", "AGENTMUX_LOG_LEVEL"),
		"diagnostic log level",
	)

	root.AddCommand(
		newDispatchCommand(&socket, &sessionName),
		newListCommand(&socket, &sessionName),
		newAttachCommand(&socket, &sessionName),
		newSendCommand(&socket, &sessionName),
		newStopCommand(&socket, &sessionName),
		newDeleteCommand(&socket, &sessionName),
		newEventCommand(&socket, &sessionName),
		newProviderEventCommand(&socket, &sessionName),
		newProviderPermissionCommand(&socket, &sessionName),
		newLogsCommand(&logFile),
		newRunCommand(&socket, &sessionName),
	)
	return root
}

func shouldHostDashboard() bool {
	return os.Getenv("TMUX") == "" && os.Getenv(dashboardHostedEnv) == ""
}

func runDashboard(socket, sessionName string) error {
	service, err := newService(socket, sessionName)
	if err != nil {
		return err
	}
	program := tea.NewProgram(ui.NewModel(service), tea.WithAltScreen())
	_, err = program.Run()
	return err
}

func hostDashboard(command *cobra.Command, tmuxPath, socket string) error {
	executable, err := os.Executable()
	if err != nil {
		return fmt.Errorf("find Runstead executable: %w", err)
	}
	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("find dashboard directory: %w", err)
	}

	session := dashboardSessionName(os.Getpid(), time.Now())
	shellCommand := dashboardShellCommand(executable, os.Args[1:])
	args := dashboardHostArgs(socket, session, cwd, shellCommand)
	process := exec.Command(tmuxPath, args...)
	process.Stdin = command.InOrStdin()
	process.Stdout = command.OutOrStdout()
	process.Stderr = command.ErrOrStderr()

	defer cleanupDashboardSession(tmuxPath, socket, session)
	if err := process.Run(); err != nil {
		return fmt.Errorf("host dashboard in tmux: %w", err)
	}
	return nil
}

func dashboardSessionName(pid int, now time.Time) string {
	return fmt.Sprintf(
		"runstead-ui-%d-%06x",
		pid,
		uint64(now.UnixNano())&0xffffff,
	)
}

func dashboardShellCommand(executable string, args []string) string {
	command := append([]string{executable}, args...)
	return dashboardHostedEnv + "=1 exec " + shellJoinCommand(command)
}

func dashboardHostArgs(socket, session, cwd, shellCommand string) []string {
	args := make([]string, 0, 10)
	if socket != "" {
		args = append(args, "-L", socket)
	}
	return append(
		args,
		"new-session",
		"-s", session,
		"-c", cwd,
		"-n", "runstead",
		shellCommand,
	)
}

func configureHostedDashboard(socket string) {
	if os.Getenv(dashboardHostedEnv) == "" || os.Getenv("TMUX") == "" {
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

func newService(socket, sessionName string) (*app.Service, error) {
	client := tmux.NewClient(socket)
	runtime, err := tmux.NewRuntime(client, sessionName)
	if err != nil {
		return nil, err
	}
	return app.NewService(runtime, provider.NewRegistry(), workspace.NewRegistry()), nil
}

func newDispatchCommand(socket, sessionName *string) *cobra.Command {
	var providerName string
	var cwd string
	var name string

	command := &cobra.Command{
		Use:   "dispatch [task]",
		Short: "Start an agent in a managed tmux window",
		Args:  cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			task, err := readTask(args)
			if err != nil {
				return err
			}
			service, err := newService(*socket, *sessionName)
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
			})
			if err != nil {
				return err
			}
			fmt.Printf("%s\t%s\t%s\n", managedAgent.ID, managedAgent.Name, managedAgent.WindowID)
			return nil
		},
	}
	command.Flags().StringVarP(&providerName, "provider", "p", string(agent.ProviderCodex), "provider: codex, claude, or shell")
	command.Flags().StringVarP(&cwd, "cwd", "C", "", "working directory")
	command.Flags().StringVarP(&name, "name", "n", "", "tmux window name")
	return command
}

func newListCommand(socket, sessionName *string) *cobra.Command {
	var asJSON bool
	command := &cobra.Command{
		Use:   "list",
		Short: "List managed agents",
		RunE: func(cmd *cobra.Command, args []string) error {
			service, err := newService(*socket, *sessionName)
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

func newAttachCommand(socket, sessionName *string) *cobra.Command {
	return &cobra.Command{
		Use:   "attach <id>",
		Short: "Switch the current tmux client to an agent",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			service, err := newService(*socket, *sessionName)
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

func newSendCommand(socket, sessionName *string) *cobra.Command {
	return &cobra.Command{
		Use:   "send <id> <message>",
		Short: "Send a message to an agent",
		Args:  cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			service, err := newService(*socket, *sessionName)
			if err != nil {
				return err
			}
			return service.Send(cmd.Context(), args[0], strings.Join(args[1:], " "))
		},
	}
}

func newStopCommand(socket, sessionName *string) *cobra.Command {
	return &cobra.Command{
		Use:   "stop <id>",
		Short: "Interrupt an agent",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			service, err := newService(*socket, *sessionName)
			if err != nil {
				return err
			}
			return service.Interrupt(cmd.Context(), args[0])
		},
	}
}

func newDeleteCommand(socket, sessionName *string) *cobra.Command {
	return &cobra.Command{
		Use:   "delete <id>",
		Short: "Delete an agent tmux window",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			service, err := newService(*socket, *sessionName)
			if err != nil {
				return err
			}
			return service.Delete(cmd.Context(), args[0])
		},
	}
}

func newEventCommand(socket, sessionName *string) *cobra.Command {
	var id string
	var state string
	var attention string
	var summary string

	command := &cobra.Command{
		Use:   "event",
		Short: "Update agent state from a provider hook",
		RunE: func(cmd *cobra.Command, args []string) error {
			if id == "" {
				id = envFirst("RUNSTEAD_ID", "AGENTMUX_ID")
			}
			if id == "" {
				return fmt.Errorf("agent id is required; pass --id or run inside a Runstead agent")
			}
			activity, err := parseActivity(state)
			if err != nil {
				return err
			}
			attentionKind, err := parseAttention(attention)
			if err != nil {
				return err
			}
			service, err := newService(*socket, *sessionName)
			if err != nil {
				return err
			}
			return service.Update(cmd.Context(), id, tmux.Update{
				Activity:  activity,
				Attention: attentionKind,
				Summary:   summary,
			})
		},
	}
	command.Flags().StringVar(&id, "id", "", "agent id")
	command.Flags().StringVar(&state, "state", "", "starting, working, idle, completed, failed, or stopped")
	command.Flags().StringVar(&attention, "attention", "", "none, question, approval, or auth")
	command.Flags().StringVar(&summary, "summary", "", "one-line status summary")
	return command
}

func newProviderEventCommand(socket, sessionName *string) *cobra.Command {
	return &cobra.Command{
		Use:    "_provider-event <provider> [payload]",
		Hidden: true,
		Args:   cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			id := envFirst("RUNSTEAD_ID", "AGENTMUX_ID")
			if id == "" {
				return nil
			}

			var payload []byte
			if len(args) == 2 {
				payload = []byte(args[1])
			} else {
				var err error
				payload, err = io.ReadAll(cmd.InOrStdin())
				if err != nil {
					return nil
				}
			}
			event, handled, err := provider.ParseEvent(agent.Provider(args[0]), payload)
			if err != nil || !handled {
				return nil
			}
			service, err := newService(*socket, *sessionName)
			if err != nil {
				return nil
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 5*time.Second)
			defer cancel()
			_ = service.Update(ctx, id, tmux.Update{
				Activity:  event.Activity,
				Attention: event.Attention,
				Summary:   event.Summary,
			})
			return nil
		},
	}
}

func newProviderPermissionCommand(socket, sessionName *string) *cobra.Command {
	return &cobra.Command{
		Use:    "_provider-permission <provider>",
		Hidden: true,
		Args:   cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id := envFirst("RUNSTEAD_ID", "AGENTMUX_ID")
			if id == "" {
				return nil
			}
			payload, err := io.ReadAll(cmd.InOrStdin())
			if err != nil {
				diagnostic.Logger().Warn(
					"permission hook input unavailable",
					"error", err,
				)
				return nil
			}
			bridge, err := provider.ParsePermissionRequest(
				agent.Provider(args[0]),
				id,
				payload,
			)
			if err != nil {
				diagnostic.Logger().Warn(
					"permission hook payload unsupported",
					"provider", args[0],
					"error", err,
				)
				return nil
			}

			store := pending.NewStore()
			published, err := store.Publish(bridge.Action)
			if err != nil {
				diagnostic.Logger().Warn(
					"permission request publication failed",
					"agent_id", id,
					"error", err,
				)
				return nil
			}
			bridge.Action = published
			defer func() {
				if err := store.Remove(published.ID); err != nil {
					diagnostic.Logger().Warn(
						"permission request cleanup failed",
						"action_id", published.ID,
						"error", err,
					)
				}
			}()

			service, serviceErr := newService(*socket, *sessionName)
			if serviceErr == nil {
				updatePermissionAgent(
					service,
					id,
					agent.ActivityIdle,
					agent.AttentionApproval,
					published.Title,
				)
			}

			resolution, err := store.Wait(cmd.Context(), published.ID)
			if errors.Is(err, pending.ErrNoController) {
				diagnostic.Logger().Info(
					"permission request returned to provider terminal",
					"agent_id", id,
					"action_id", published.ID,
				)
				return nil
			}
			if err != nil {
				diagnostic.Logger().Warn(
					"permission request wait failed",
					"agent_id", id,
					"action_id", published.ID,
					"error", err,
				)
				return nil
			}

			response, handled, err := bridge.Response(resolution)
			if err != nil {
				diagnostic.Logger().Warn(
					"permission response failed",
					"agent_id", id,
					"action_id", published.ID,
					"error", err,
				)
				return nil
			}
			if !handled {
				if serviceErr == nil {
					updatePermissionAgent(
						service,
						id,
						agent.ActivityIdle,
						agent.AttentionApproval,
						"Review permission in terminal",
					)
				}
				return nil
			}
			if _, err := fmt.Fprintln(cmd.OutOrStdout(), string(response)); err != nil {
				return fmt.Errorf("write permission response: %w", err)
			}

			if serviceErr == nil {
				summary := "Permission approved"
				if resolution.OptionID == pending.OptionDeny {
					summary = "Permission denied"
				}
				updatePermissionAgent(
					service,
					id,
					agent.ActivityWorking,
					agent.AttentionNone,
					summary,
				)
			}
			return nil
		},
	}
}

func updatePermissionAgent(
	service *app.Service,
	id string,
	activity agent.Activity,
	attention agent.Attention,
	summary string,
) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := service.Update(ctx, id, tmux.Update{
		Activity:  activity,
		Attention: attention,
		Summary:   summary,
	}); err != nil {
		diagnostic.Logger().Warn(
			"permission state update failed",
			"agent_id", id,
			"error", err,
		)
	}
}

func newRunCommand(socket, sessionName *string) *cobra.Command {
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
			service, err := newService(*socket, *sessionName)
			if err != nil {
				return err
			}
			updateCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			_ = service.Update(updateCtx, id, tmux.Update{Activity: agent.ActivityWorking})
			cancel()

			child := exec.Command(launch.Path, launch.Args...)
			child.Dir = cwd
			child.Stdin = os.Stdin
			child.Stdout = os.Stdout
			child.Stderr = os.Stderr
			child.Env = append(os.Environ(),
				"RUNSTEAD_ID="+id,
				"RUNSTEAD_WINDOW="+window,
				"RUNSTEAD_BIN="+os.Args[0],
				"RUNSTEAD_SESSION="+*sessionName,
				"RUNSTEAD_TMUX_SOCKET="+*socket,
				// Keep legacy hook integrations working during the rename.
				"AGENTMUX_ID="+id,
				"AGENTMUX_WINDOW="+window,
				"AGENTMUX_BIN="+os.Args[0],
				"AGENTMUX_SESSION="+*sessionName,
				"AGENTMUX_TMUX_SOCKET="+*socket,
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
			_ = service.Update(updateCtx, id, tmux.Update{Activity: activity})
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
	case "", agent.AttentionQuestion, agent.AttentionApproval, agent.AttentionAuth:
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
