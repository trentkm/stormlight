package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/trentkm/stormlight/internal/api"
	"github.com/trentkm/stormlight/internal/config"
	"github.com/trentkm/stormlight/internal/diagnostic"
)

// newServeCommand runs the HTTP API: the same domain the dashboard drives,
// reachable by a second client.
//
// It binds loopback and nothing else. These routes dispatch agents and
// stream terminals in every workspace the catalog knows, which is shell
// access to all of them — reaching them from another machine is a tunnel
// the operator opens deliberately (`ssh -L`, Tailscale), never a default
// this command chose for them.
func newServeCommand(cfg config.Config) *cobra.Command {
	var port int
	var token string

	command := &cobra.Command{
		Use:   "serve",
		Short: "Serve the HTTP API for web clients",
		RunE: func(cmd *cobra.Command, args []string) error {
			service, err := newService(cfg)
			if err != nil {
				return err
			}
			if token == "" {
				if token, err = api.NewToken(); err != nil {
					return err
				}
			}
			server, err := api.New(service, token)
			if err != nil {
				return err
			}

			listener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
			if err != nil {
				return fmt.Errorf("listen: %w", err)
			}
			stopEvents := server.Run()
			defer stopEvents()

			httpServer := &http.Server{
				Handler: server.Handler(),
				// No write timeout: a terminal socket and an event stream
				// are meant to outlive any request. Reading a request head
				// is still bounded, so a connection that never speaks
				// cannot hold a slot forever.
				ReadHeaderTimeout: 10 * time.Second,
			}

			address := fmt.Sprintf("http://%s/?token=%s", listener.Addr(), token)
			fmt.Fprintf(cmd.OutOrStdout(), "Stormlight API on %s\n", address)
			diagnostic.Logger().Info("api serving", "address", listener.Addr().String())

			// Ctrl-C should close the listener and let in-flight
			// attachments end, rather than killing the process out from
			// under a terminal someone is typing into.
			ctx, stop := signal.NotifyContext(cmd.Context(),
				os.Interrupt, syscall.SIGTERM)
			defer stop()
			done := make(chan error, 1)
			go func() { done <- httpServer.Serve(listener) }()

			select {
			case err := <-done:
				if errors.Is(err, http.ErrServerClosed) {
					return nil
				}
				return err
			case <-ctx.Done():
				shutdownCtx, cancel := context.WithTimeout(
					context.Background(), 5*time.Second)
				defer cancel()
				return httpServer.Shutdown(shutdownCtx)
			}
		},
	}
	command.Flags().IntVar(&port, "port", 7331, "loopback port to listen on")
	command.Flags().StringVar(&token, "token", "",
		"access token; generated per run when unset")
	return command
}
