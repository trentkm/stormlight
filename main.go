package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
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
	"github.com/trentkm/stormlight/internal/fleet"
	"github.com/trentkm/stormlight/internal/history"
	"github.com/trentkm/stormlight/internal/picker"
	"github.com/trentkm/stormlight/internal/provider"
	"github.com/trentkm/stormlight/internal/remote"
	"github.com/trentkm/stormlight/internal/selfpath"
	"github.com/trentkm/stormlight/internal/session"
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
	// A protocol mismatch across a bridge should name both sides, and
	// only main knows what this one is.
	remote.SetLocalVersion(version)

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
		newWorkspaceCommand(cfg),
		newRemoteCommand(cfg),
		newEventCommand(cfg),
		newProviderEventCommand(cfg),
		newLogsCommand(&logFile),
		newConfigCommand(cfg),
		newWindrunnerDaemonCommand(),
		newWindrunnerAttachCommand(),
		newWindrunnerBridgeCommand(),
		newResolveCommand(),
		newReadCommand(),
		newPickCommand(),
		newHistoryCommand(),
		newBenchCommand(),
		newServeCommand(cfg),
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

// newWindrunnerBridgeCommand is the far side of a remote dashboard's
// connection: the process `ssh <host> stormlight _wrbridge` starts. It
// makes sure this machine has a daemon — nothing else can, from where the
// dashboard is standing — says who it is, and then becomes a pipe onto
// that daemon's socket.
//
// Everything after the greeting line is the windrunner wire protocol
// verbatim, so nothing may be written to stdout but the bridge itself.
// Diagnostics go to the log file, as everywhere else.
func newWindrunnerBridgeCommand() *cobra.Command {
	return &cobra.Command{
		Use:    "_wrbridge",
		Hidden: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			binPath, err := selfpath.Resolve()
			if err != nil {
				return err
			}
			// Ensuring the daemon is the reason this runs as a program
			// rather than as a forwarded socket: starting one means
			// running a process on this host.
			c, err := wrclient.EnsureDaemon(
				windrun.SocketPath(), []string{binPath, "_wrdaemon"}, 5*time.Second)
			if err != nil {
				return err
			}
			c.Close()
			hostname, _ := os.Hostname()
			return remote.Serve(windrun.SocketPath(), remote.Hello{
				Protocol:  remote.Protocol,
				Version:   version,
				Bin:       binPath,
				SocketDir: windrun.SocketDir(),
				Hostname:  hostname,
			}, os.Stdin, os.Stdout)
		},
	}
}

// newResolveCommand answers what a directory belongs to, on this machine.
// A dashboard on another machine runs it over SSH, because resolution is
// filesystem work — `git rev-parse`, a directory that has to exist, an
// executable resolver in this host's own config — and none of that can be
// answered from somewhere else. The reply is the workspace context as
// JSON, with --roots for every runnable checkout in the same workspace.
func newResolveCommand() *cobra.Command {
	var roots bool
	command := &cobra.Command{
		Use:    "_resolve <path>",
		Hidden: true,
		Args:   cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			registry := workspace.NewRegistry()
			value, err := registry.Resolve(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			encoder := json.NewEncoder(cmd.OutOrStdout())
			if !roots {
				return encoder.Encode(value)
			}
			values, err := registry.ExecutionRoots(cmd.Context(), value)
			if err != nil {
				return err
			}
			return encoder.Encode(values)
		},
	}
	command.Flags().BoolVar(&roots, "roots", false,
		"report every runnable checkout in the workspace")
	return command
}

// newReadCommand copies a file to stdout, for a dashboard on another
// machine. An agent's transcript is written by its provider beside its
// repository, so the path in an agent's record names a file only that
// host can open.
//
// The size cap is applied here rather than by the reader, because a cap
// on the far side of a tunnel is one that has already paid for every byte
// it discards.
func newReadCommand() *cobra.Command {
	return &cobra.Command{
		Use:    "_read <path>",
		Hidden: true,
		Args:   cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			file, err := os.Open(args[0])
			if err != nil {
				return err
			}
			defer file.Close()
			_, err = io.Copy(cmd.OutOrStdout(), io.LimitReader(file, windrun.MaxAgentFile))
			return err
		},
	}
}

// newPickCommand is the directory chooser, running on the machine whose
// directories are being chosen.
//
// It answers through its own windrunner session rather than through a
// file: Yazi writes its choice beside itself, and on another machine that
// is a path the dashboard cannot read. The session is the one thing both
// ends already hold, so the answer goes into its metadata — written to
// the daemon on this machine, read by the dashboard over the tunnel it
// already has.
func newPickCommand() *cobra.Command {
	var yaziPath string
	command := &cobra.Command{
		Use:    "_pick <start-directory>",
		Hidden: true,
		Args:   cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			start := ""
			if len(args) == 1 {
				start = args[0]
			}
			chosen, err := picker.Choose(yaziPath, start)
			if err != nil {
				// The popup closes with this process, taking its stderr
				// with it, so the reason goes where the dashboard can
				// still read it.
				_ = recordPickFailure(err)
				return err
			}
			return recordPick(chosen)
		},
	}
	command.Flags().StringVar(&yaziPath, "yazi", "",
		"path to yazi; found on PATH when unset")
	return command
}

// recordPickFailure records why there is no answer, so the dashboard can
// say "yazi is not installed on devbox" rather than "exited with status
// 1".
func recordPickFailure(reason error) error {
	return writeOverlayMetadata(windrun.OverlayErrorKey, reason.Error())
}

// recordPick writes the answer into this process's own session. Quitting
// without choosing records nothing, which is how the dashboard tells a
// cancelled picker from one that chose.
func recordPick(chosen string) error {
	if strings.TrimSpace(chosen) == "" {
		return nil
	}
	return writeOverlayMetadata(windrun.OverlayResultKey, chosen)
}

// writeOverlayMetadata leaves a value in this process's own session, for
// whoever is holding the other end of it.
func writeOverlayMetadata(key, value string) error {
	sessionID := os.Getenv("WINDRUNNER_SESSION")
	if sessionID == "" {
		// Run by hand rather than as an overlay: say it instead of
		// filing it.
		fmt.Println(value)
		return nil
	}
	c, err := wrclient.Dial(windrun.SocketPath())
	if err != nil {
		return fmt.Errorf("reach the daemon holding this session: %w", err)
	}
	defer c.Close()
	info, err := c.Info(sessionID)
	if err != nil {
		return err
	}
	metadata := info.Metadata
	if metadata == nil {
		metadata = map[string]string{}
	}
	metadata[key] = value
	return c.SetMetadata(sessionID, metadata)
}

// newHistoryCommand hands this machine's conversation log to a dashboard
// on another one. The log is written where the agent ran, so a machine's
// own copy is the only copy, and reopening a conversation that happened
// there means asking it.
func newHistoryCommand() *cobra.Command {
	return &cobra.Command{
		Use:    "_history",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			records, err := history.NewLog().Records()
			if err != nil {
				return err
			}
			encoder := json.NewEncoder(cmd.OutOrStdout())
			for _, record := range records {
				if err := encoder.Encode(record); err != nil {
					return err
				}
			}
			return nil
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
		YaziPath: cfg.Tools.Yazi,
		NvimPath: cfg.Tools.Nvim,
		Keys: ui.KeyBindings{
			AgentsNext:     cfg.Keys.AgentsNext,
			AgentsPrevious: cfg.Keys.AgentsPrevious,
			QueueNext:      cfg.Keys.QueueNext,
			QueuePrevious:  cfg.Keys.QueuePrevious,
			Zoom:           cfg.Keys.Zoom,
		},
		DefaultProvider: agent.Provider(cfg.Defaults.Provider),
		ExpandedRows:    cfg.UI.Rows == "expanded",
		ModeForDir:      cfg.ModeForDir,
		ProviderForDir:  cfg.ProviderForDir,
		Columns:         ui.LoadColumnPrefs(),
		// The machines to offer when adding a workspace: the ones the
		// user's SSH configuration names, plus any they have configured
		// here. Naming one is what makes it usable, so this list is
		// suggestions rather than permission.
		Hosts: dashboardHosts(cfg),
	}
	if openPath != "" {
		value, err := service.AddWorkspace(context.Background(), "", openPath)
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
		ui.NewModelWithOptions(service, options),
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
	// One catalog, shared: it is also the standing record of which
	// machines this dashboard works, which the runtime needs before
	// anything has named one.
	catalog := workspace.NewCatalog()
	runtime, err := newRuntime(cfg, catalog)
	if err != nil {
		return nil, err
	}
	registry := provider.NewRegistryWithSpecs(providerSpecs(cfg))
	// Resolution follows a path to the machine that has it, so the
	// registry needs the same host list the runtime has.
	return app.NewServiceWithCatalog(
		runtime, registry, workspacesWithHosts(cfg), catalog, history.NewLog(),
	), nil
}

// newRuntime builds the fleet: this machine's daemon, and one per
// configured host. It is always a fleet, even with no hosts configured,
// so there is one shape of runtime to reason about and one set of paths
// to test; a fleet of one routes straight through to its only member.
//
// Members connect lazily and on their own schedule. A host that is asleep
// or unreachable costs the dashboard its absence and nothing else — never
// a failed refresh, and never a frame spent waiting on SSH.
func newRuntime(cfg config.Config, catalog *workspace.Catalog) (session.Runtime, error) {
	members := []fleet.Member{{
		Host:    "",
		Connect: func() (session.Runtime, error) { return windrun.NewRuntime() },
	}}
	for _, name := range fleetHosts(cfg, catalog) {
		members = append(members, remoteMember(cfg, name))
	}
	// Any other host joins the moment something names it — a workspace on
	// it, a dispatch aimed at it, a name picked out of ~/.ssh/config.
	// Configuration is how a host differs from its name, not the list of
	// which hosts there are.
	discover := func(host string) fleet.Member { return remoteMember(cfg, host) }
	return fleet.New(discover, members...), nil
}

// fleetHosts are the machines to reach at start-up: the ones
// configuration customises, and the ones the catalog says hold a
// workspace. The catalog is the answer to "whose agents belong in my
// roster" — listing names no host, so without it a machine's agents would
// be invisible until something happened to mention it.
func fleetHosts(cfg config.Config, catalog *workspace.Catalog) []string {
	hosts := slices.Sorted(maps.Keys(cfg.Hosts))
	entries, err := catalog.Entries()
	if err != nil {
		diagnostic.Logger().Warn("workspace catalog unavailable", "error", err)
		return hosts
	}
	for _, entry := range entries {
		if entry.Host != "" && !slices.Contains(hosts, entry.Host) {
			hosts = append(hosts, entry.Host)
		}
	}
	return hosts
}

// dashboardHosts is every machine worth offering, in a stable order:
// what ~/.ssh/config names first, then anything configured here that it
// did not.
func dashboardHosts(cfg config.Config) []ui.HostChoice {
	var choices []ui.HostChoice
	named := map[string]bool{}
	for _, host := range remote.KnownHosts() {
		named[host.Name] = true
		choices = append(choices, ui.HostChoice{
			Name:    host.Name,
			Summary: host.Summary(),
		})
	}
	for _, name := range slices.Sorted(maps.Keys(cfg.Hosts)) {
		if named[name] {
			continue
		}
		summary := cfg.Hosts[name].Destination
		if summary == name {
			summary = ""
		}
		choices = append(choices, ui.HostChoice{Name: name, Summary: summary})
	}
	return choices
}

func remoteMember(cfg config.Config, name string) fleet.Member {
	host := remoteHostFrom(name, cfg.Hosts[name])
	return fleet.Member{
		Host:    name,
		Connect: func() (session.Runtime, error) { return windrun.NewRemoteRuntime(host) },
	}
}

func remoteHostFrom(name string, host config.Host) remote.Host {
	return remote.Host{
		Name:        name,
		Destination: host.Destination,
		Bin:         host.Bin,
		Options:     host.Options,
		NoMultiplex: host.NoMultiplex,
	}
}

// newWorkspaceService is the runtime-free service the workspace commands
// use: they read and edit the catalog and never touch an agent. It still
// needs the host list, because a catalog entry can name another machine
// and only that machine can resolve it.
func newWorkspaceService(cfg config.Config) *app.Service {
	return app.NewService(
		nil,
		provider.NewRegistry(),
		workspacesWithHosts(cfg),
	)
}

func workspacesWithHosts(cfg config.Config) *workspace.Registry {
	workspaces := workspace.NewRegistry()
	for _, name := range slices.Sorted(maps.Keys(cfg.Hosts)) {
		workspaces.AddHost(remoteHostFrom(name, cfg.Hosts[name]))
	}
	return workspaces
}

func newDispatchCommand(cfg config.Config) *cobra.Command {
	var providerName string
	var cwd string
	var name string
	var modeName string
	var host string

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
			// The per-directory overrides read this machine's filesystem,
			// so they only apply to a dispatch that lands on it.
			if overrideDir, dirErr := dispatchDirectory(cwd); dirErr == nil && host == "" {
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
				Host:     host,
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
	command.Flags().StringVar(&host, "host", "",
		"run on a configured host instead of this machine; --cwd is a path there")
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

// newRemoteCommand groups what a machine needs before it can hold
// agents. Reaching a host is one binary and one optional tool, and the
// difference between "it does not work" and "it needs yazi" is worth a
// command rather than a guess.
func newRemoteCommand(cfg config.Config) *cobra.Command {
	command := &cobra.Command{
		Use:   "remote",
		Short: "Inspect and prepare machines Stormlight can reach",
	}
	command.AddCommand(newRemoteListCommand(cfg), newRemoteSetupCommand(cfg))
	return command
}

func newRemoteListCommand(cfg config.Config) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List the machines this dashboard would offer",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			hosts := dashboardHosts(cfg)
			if len(hosts) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(),
					"No machines in ~/.ssh/config or [hosts.*]. Any name works anyway.")
				return nil
			}
			for _, host := range hosts {
				fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\n", host.Name, host.Summary)
			}
			return nil
		},
	}
}

func newRemoteSetupCommand(cfg config.Config) *cobra.Command {
	var install bool
	var withYazi bool
	var wait bool
	command := &cobra.Command{
		Use:   "setup <host>",
		Short: "Report what a machine is missing, and optionally install it",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			host := remoteHostFrom(args[0], cfg.Hosts[args[0]])
			transport := remote.NewTransport(host)
			out := cmd.OutOrStdout()

			// Every failure from here is one the person who asked has to
			// read, and in a popup this process is the only thing holding
			// the window open. So it reports, waits, and only then
			// returns the error.
			err := setUpHost(cmd.Context(), transport, args[0], out, install, withYazi)
			if err != nil {
				fmt.Fprintf(out, "\n%v\n", err)
			}
			holdOpen(out, wait)
			return err
		},
	}
	command.Flags().BoolVar(&install, "install", false,
		"install what is missing rather than only reporting it")
	command.Flags().BoolVar(&withYazi, "yazi", false,
		"also install yazi, using the host's own package manager")
	command.Flags().BoolVar(&wait, "wait", false,
		"hold the terminal open at the end, for a popup nobody else closes")
	return command
}

// setUpHost reports what a machine has and, when asked, gives it what it
// is missing.
func setUpHost(
	ctx context.Context,
	transport *remote.Transport,
	host string,
	out io.Writer,
	install, withYazi bool,
) error {
	report, err := remote.Probe(ctx, transport)
	if err != nil {
		return err
	}
	writeRemoteReport(out, report)
	if !install {
		if !report.Ready() || !report.Yazi.Present() {
			fmt.Fprintf(out, "\nRun with --install to fix this.\n")
		}
		return nil
	}

	if !report.Stormlight.Present() {
		binary, err := selfpath.Resolve()
		if err != nil {
			return err
		}
		installed, err := remote.InstallStormlight(ctx, transport, report, binary, out)
		if err != nil {
			return err
		}
		// A non-interactive SSH shell often has no ~/.local/bin on its
		// PATH, so the bridge would look for a binary by name and not
		// find the one just put there. Naming it outright is what
		// [hosts.*] is for.
		recordHostBinary(out, host, installed)
	}
	if withYazi && !report.Yazi.Present() {
		if err := remote.InstallYazi(ctx, transport, report, out); err != nil {
			return err
		}
	}
	fresh, err := remote.Probe(ctx, transport)
	if err != nil {
		return err
	}
	fmt.Fprintln(out)
	writeRemoteReport(out, fresh)
	return nil
}

// recordHostBinary writes the installed path into the host's
// configuration, so the next connection finds it whatever that machine's
// non-interactive PATH happens to hold.
//
// It only appends a section that is not there. Rewriting the file would
// mean parsing and re-emitting it, which costs the comments and the
// ordering someone put there by hand — so an existing entry is left alone
// and the line to change is printed instead.
func recordHostBinary(out io.Writer, host, installed string) {
	path := config.Path()
	if path == "" {
		return
	}
	existing, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		fmt.Fprintf(out, "\nSet bin = %q in [hosts.%s]: %v\n", installed, host, err)
		return
	}
	section := fmt.Sprintf("[hosts.%s]", host)
	if strings.Contains(string(existing), section) {
		fmt.Fprintf(out, "\n%s already exists — set bin = %q in it if the bridge "+
			"cannot find stormlight.\n", section, installed)
		return
	}
	entry := fmt.Sprintf("\n%s\nbin = %q\n", section, installed)
	if err := appendConfig(path, entry); err != nil {
		fmt.Fprintf(out, "\nAdd this to %s:\n%s\n", path, entry)
		return
	}
	fmt.Fprintf(out, "\nRecorded bin = %q in %s\n", installed, path)
}

func appendConfig(path, entry string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer file.Close()
	_, err = file.WriteString(entry)
	return err
}

// holdOpen keeps a popup up long enough to be read. The overlay closes
// when its program exits, and a report that scrolls past in a tenth of a
// second is a report nobody has seen.
func holdOpen(out io.Writer, wait bool) {
	if !wait {
		return
	}
	fmt.Fprintf(out, "\nPress any key to close.\n")
	var one [1]byte
	_, _ = os.Stdin.Read(one[:])
}

func writeRemoteReport(out io.Writer, report remote.Report) {
	fmt.Fprintf(out, "%s\t%s\n", report.Host, report.Platform)
	line := func(requirement remote.Requirement, note string) {
		state := "missing"
		detail := note
		if requirement.Present() {
			state = "ok"
			detail = requirement.Path
			if requirement.Version != "" {
				detail += "  " + requirement.Version
			}
		}
		fmt.Fprintf(out, "  %-11s %-8s %s\n", requirement.Name, state, detail)
	}
	line(report.Stormlight, "required — the bridge, the resolver and the picker are all this binary")
	yaziNote := "optional — browsing needs it; a typed path does not"
	if !report.Yazi.Present() && report.YaziInstallCommand() != "" {
		yaziNote = "optional — " + report.YaziInstallCommand()
	}
	line(report.Yazi, yaziNote)
}

func newWorkspaceCommand(cfg config.Config) *cobra.Command {
	command := &cobra.Command{
		Use:   "workspace",
		Short: "Inspect and manage workspaces",
	}
	command.AddCommand(
		newWorkspaceAddCommand(cfg),
		newWorkspaceListCommand(cfg),
		newWorkspaceRootsCommand(cfg),
	)
	return command
}

func newWorkspaceAddCommand(cfg config.Config) *cobra.Command {
	var asJSON bool
	var host string
	command := &cobra.Command{
		Use:   "add <path>",
		Short: "Add a workspace to the dashboard",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			service := newWorkspaceService(cfg)
			ctx, cancel := context.WithTimeout(cmd.Context(), 10*time.Second)
			defer cancel()
			value, err := service.AddWorkspace(ctx, host, args[0])
			if err != nil {
				return err
			}
			if asJSON {
				return encodeJSON(cmd.OutOrStdout(), value)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\n", value.Name, value.Root)
			return nil
		},
	}
	command.Flags().BoolVar(&asJSON, "json", false, "emit JSON")
	command.Flags().StringVar(&host, "host", "",
		"the configured host the path is on; omit for this machine")
	return command
}

func newWorkspaceListCommand(cfg config.Config) *cobra.Command {
	var asJSON bool
	command := &cobra.Command{
		Use:   "list",
		Short: "List catalog workspaces",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			service := newWorkspaceService(cfg)
			ctx, cancel := context.WithTimeout(cmd.Context(), 10*time.Second)
			defer cancel()
			values, err := service.ListWorkspaces(ctx)
			if err != nil {
				return err
			}
			return writeWorkspaces(cmd.OutOrStdout(), values, asJSON)
		},
	}
	command.Flags().BoolVar(&asJSON, "json", false, "emit JSON")
	return command
}

func newWorkspaceRootsCommand(cfg config.Config) *cobra.Command {
	var asJSON bool
	command := &cobra.Command{
		Use:   "roots [path]",
		Short: "List runnable roots in a workspace",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			service := newWorkspaceService(cfg)
			ctx, cancel := context.WithTimeout(cmd.Context(), 10*time.Second)
			defer cancel()
			var values []workspace.Context
			var err error
			if len(args) == 1 {
				values, err = service.WorkspaceRoots(ctx, args[0])
			} else {
				values, err = service.WorkspaceRoots(ctx, "")
			}
			if err != nil {
				return err
			}
			return writeWorkspaces(cmd.OutOrStdout(), values, asJSON)
		},
	}
	command.Flags().BoolVar(&asJSON, "json", false, "emit JSON")
	return command
}

func writeWorkspaces(out io.Writer, values []workspace.Context, asJSON bool) error {
	if asJSON {
		return encodeJSON(out, values)
	}
	if len(values) == 0 {
		fmt.Fprintln(out, "No workspaces.")
		return nil
	}
	fmt.Fprintf(out, "%-12s %-24s %s\n", "KIND", "NAME", "PATH")
	for _, value := range values {
		fmt.Fprintf(out, "%-12s %-24s %s\n",
			value.Kind,
			truncatePlain(value.Name, 24),
			value.ExecutionRoot,
		)
	}
	return nil
}

func encodeJSON(out io.Writer, value any) error {
	encoder := json.NewEncoder(out)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
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
