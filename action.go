package main

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"

	"github.com/spf13/cobra"
	actionplugin "github.com/trentkm/stormlight/internal/action"
	"github.com/trentkm/stormlight/internal/agent"
	"github.com/trentkm/stormlight/internal/session"
	"github.com/trentkm/stormlight/internal/windrun"
)

func newActionCommand() *cobra.Command {
	var id string
	command := &cobra.Command{
		Use:   "action <plugin> [args...]",
		Short: "Ask the dashboard to run an installed action plugin",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if id == "" {
				id = agentIDFromEnv()
			}
			if id == "" {
				return fmt.Errorf(
					"agent id is required; run inside a Stormlight-managed agent or pass --id",
				)
			}
			directory, err := os.Getwd()
			if err != nil {
				return fmt.Errorf("get current directory: %w", err)
			}
			name := args[0]
			payload, err := actionplugin.NewRegistry().Prepare(
				cmd.Context(),
				name,
				directory,
				args[1:],
			)
			if err != nil {
				return err
			}
			requestID, err := dashboardActionRequestID()
			if err != nil {
				return err
			}
			runtime, err := windrun.NewRuntime()
			if err != nil {
				return err
			}
			request := agent.DashboardActionRequest{
				ID:      requestID,
				Name:    name,
				Payload: payload,
			}
			if err := runtime.Update(
				cmd.Context(),
				id,
				session.Update{DashboardAction: &request},
			); err != nil {
				return err
			}
			fmt.Fprintf(
				cmd.OutOrStdout(),
				"Queued dashboard action %q.\n",
				name,
			)
			return nil
		},
	}
	command.Flags().StringVar(
		&id,
		"id",
		"",
		"managed agent id; defaults to STORMLIGHT_ID",
	)
	return command
}

func dashboardActionRequestID() (string, error) {
	var value [8]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", fmt.Errorf("create dashboard action request id: %w", err)
	}
	return hex.EncodeToString(value[:]), nil
}
