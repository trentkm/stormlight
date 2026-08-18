package api

import (
	"context"
	"encoding/json"

	"net/http"
	"strconv"

	"github.com/coder/websocket"

	"github.com/trentkm/stormlight/internal/diagnostic"
	"github.com/trentkm/stormlight/internal/pty"
)

// The terminal socket is deliberately not a JSON protocol.
//
// Binary messages are terminal bytes, raw and unwrapped, in both
// directions: the daemon's exact snapshot arrives first, then live
// output; keystrokes travel back the same way. Wrapping a keypress in
// JSON would put an encode, a decode and an allocation on the path
// between a key and the process that answers it, for no gain — the frame
// already carries the length.
//
// Text messages are the control channel: a size, or a notice that the
// terminal moved under this viewer. There are few of them and they are
// never in the typing path.
type controlMessage struct {
	Type string `json:"type"`
	Cols int    `json:"cols,omitempty"`
	Rows int    `json:"rows,omitempty"`
}

const (
	controlResize = "resize"
	// controlSeed precedes a binary message that replaces the replica
	// rather than extending it: the first one at attach, and any later
	// one when the daemon resynced a viewer that fell behind.
	controlSeed = "seed"
)

// terminal relays one agent's live terminal over one WebSocket.
func (s *Server) terminal(w http.ResponseWriter, r *http.Request) {
	cols, rows := terminalSize(r)
	transport, err := s.service.AttachTerminal(r.Context(), r.PathValue("id"), cols, rows)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		// Origin is checked in the auth middleware, against the host
		// actually being served rather than a list maintained here.
		InsecureSkipVerify: true,
	})
	if err != nil {
		transport.Close()
		return
	}
	// A terminal is a long conversation; nothing about it should time out
	// for being quiet, so the relay runs on a context of its own that
	// ends when either side hangs up.
	ctx, cancel := context.WithCancel(context.WithoutCancel(r.Context()))
	defer cancel()
	defer transport.Close()
	defer conn.CloseNow()

	// The daemon's snapshot is state, not output: it replaces whatever the
	// replica had. Say so before sending it, so a reconnecting client
	// never appends a screen to the one it was already showing.
	if err := writeSeed(ctx, conn, transport.Seed()); err != nil {
		return
	}

	// A terminal this viewer shares can be resized by someone else — the
	// dashboard, a full-screen attach — and a replica painting at a size
	// nobody else is using is a diverged replica.
	if notifier, ok := transport.(pty.ResizeNotifier); ok {
		notifier.OnResize(func(cols, rows int) {
			_ = writeControl(ctx, conn, controlMessage{
				Type: controlResize, Cols: cols, Rows: rows,
			})
		})
	}

	go s.pumpInput(ctx, cancel, conn, transport)

	// Output goes out as it arrives. No coalescing here: the transport
	// hands over whole chunks already, and holding one back to merge it
	// with the next would trade the responsiveness this plane exists for
	// against nothing.
	for {
		select {
		case <-ctx.Done():
			return
		case chunk, ok := <-transport.Output():
			if !ok {
				// The session ended; let the client see a clean close
				// rather than a dropped connection.
				_ = conn.Close(websocket.StatusNormalClosure, "session ended")
				return
			}
			if err := conn.Write(ctx, websocket.MessageBinary, chunk); err != nil {
				return
			}
		}
	}
}

// pumpInput carries the client's keystrokes and resizes to the terminal.
func (s *Server) pumpInput(
	ctx context.Context,
	cancel context.CancelFunc,
	conn *websocket.Conn,
	transport pty.Transport,
) {
	defer cancel()
	for {
		kind, payload, err := conn.Read(ctx)
		if err != nil {
			return
		}
		switch kind {
		case websocket.MessageBinary:
			if err := transport.Write(payload); err != nil {
				return
			}
		case websocket.MessageText:
			var message controlMessage
			if err := json.Unmarshal(payload, &message); err != nil {
				continue
			}
			if message.Type != controlResize {
				continue
			}
			if err := transport.Resize(ctx, message.Cols, message.Rows); err != nil {
				// A resize that loses a race with the session ending is
				// not worth dropping the connection over.
				diagnostic.Logger().Debug("terminal resize failed", "error", err)
			}
		}
	}
}

func writeSeed(ctx context.Context, conn *websocket.Conn, seed []byte) error {
	if err := writeControl(ctx, conn, controlMessage{Type: controlSeed}); err != nil {
		return err
	}
	return conn.Write(ctx, websocket.MessageBinary, seed)
}

func writeControl(ctx context.Context, conn *websocket.Conn, message controlMessage) error {
	payload, err := json.Marshal(message)
	if err != nil {
		return err
	}
	return conn.Write(ctx, websocket.MessageText, payload)
}

// terminalSize reads the viewer's geometry from the query string. A
// viewer that does not say gets a conventional terminal; it will resize
// as soon as it has laid itself out.
func terminalSize(r *http.Request) (cols, rows int) {
	cols = positiveQuery(r, "cols", 80)
	rows = positiveQuery(r, "rows", 24)
	return cols, rows
}

func positiveQuery(r *http.Request, name string, fallback int) int {
	value, err := strconv.Atoi(r.URL.Query().Get(name))
	if err != nil || value < 2 {
		return fallback
	}
	return value
}
