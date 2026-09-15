package main

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"

	"github.com/spf13/cobra"
	"github.com/trentkm/stormlight/internal/agent"
	"github.com/trentkm/stormlight/internal/session"
	"github.com/trentkm/stormlight/internal/windrun"
	"github.com/trentkm/stormlight/internal/zed"
)

func newZedCommand() *cobra.Command {
	command := &cobra.Command{
		Use:   "zed",
		Short: "Open managed-agent work in Zed",
	}
	command.AddCommand(newZedDiffCommand())
	return command
}

func newZedDiffCommand() *cobra.Command {
	var id string
	command := &cobra.Command{
		Use:   "diff [path]",
		Short: "Open this agent's Git changes in Zed",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if id == "" {
				id = agentIDFromEnv()
			}
			if id == "" {
				return fmt.Errorf(
					"agent id is required; run inside a Stormlight-managed agent or pass --id",
				)
			}
			path := ""
			if len(args) == 1 {
				path = args[0]
			}
			targets, err := zed.DiffTargets(cmd.Context(), path)
			if err != nil {
				return err
			}
			requestID, err := zedDiffRequestID()
			if err != nil {
				return err
			}
			runtime, err := windrun.NewRuntime()
			if err != nil {
				return err
			}
			request := agent.ZedDiffRequest{ID: requestID, Paths: targets}
			if err := runtime.Update(
				cmd.Context(),
				id,
				session.Update{ZedDiff: &request},
			); err != nil {
				return err
			}
			repositoryWord := "repository"
			if len(targets) != 1 {
				repositoryWord = "repositories"
			}
			fmt.Fprintf(
				cmd.OutOrStdout(),
				"Queued %d changed %s for Zed.\n",
				len(targets),
				repositoryWord,
			)
			return nil
		},
	}
	command.Flags().StringVar(&id, "id", "", "managed agent id; defaults to STORMLIGHT_ID")
	return command
}

func zedDiffRequestID() (string, error) {
	var value [8]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", fmt.Errorf("create Zed diff request id: %w", err)
	}
	return hex.EncodeToString(value[:]), nil
}
