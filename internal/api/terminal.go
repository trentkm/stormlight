package api

import (
	"context"
	"encoding/json"

	"net/http"
	"strconv"
	"time"

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

	// Upgrade before attaching. Attaching resizes the agent's terminal —
	// the daemon owns one terminal per agent and every viewer shares it —
	// so doing it first would let a plain GET, which cannot become a
	// terminal at all, reflow the pane a dashboard user is reading.
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		// Origin is checked in the auth middleware, against the loopback
		// hosts this server actually serves.
		InsecureSkipVerify: true,
	})
	if err != nil {
		return
	}
	transport, err := s.service.AttachTerminal(r.Context(), r.PathValue("id"), cols, rows)
	if err != nil {
		_ = conn.Close(websocket.StatusInternalError, "attach failed")
		return
	}
	// A paste is one message, and terminals receive large ones. The
	// library's default read limit is 32 KiB, which would not reject the
	// paste but tear down the whole attachment.
	conn.SetReadLimit(maxTerminalMessage)
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

	go s.pumpInput(ctx, cancel, conn, transport)
	go keepalive(ctx, cancel, conn)

	// Output goes out as it arrives. No coalescing here: the transport
	// hands over whole chunks already, and holding one back to merge it
	// with the next would trade the responsiveness this plane exists for
	// against nothing.
	announced := pty.Size{Cols: cols, Rows: rows}
	for {
		select {
		case <-ctx.Done():
			return
		case message, ok := <-transport.Output():
			if ok && message.Resync != nil {
				// State first: a resync carries its own size, and the
				// resize branch below would otherwise swallow it.
				//
				// This viewer fell behind and the daemon sent state
				// instead of the bytes it missed. Same shape as the
				// opening seed, and for the same reason: the client
				// replaces its replica rather than appending to it. The
				// size goes first so the client sizes the replica it is
				// about to fill — while a viewer is in debt the daemon
				// sends nothing else, so this is the only place it can
				// learn the terminal moved.
				// Only when it actually moved: a resync carries the size
				// every time, and a viewer in debt gets one every tenth
				// of a second. The control channel is meant to be quiet.
				if message.Resize != nil && *message.Resize != announced {
					if err := writeControl(ctx, conn, controlMessage{
						Type: controlResize,
						Cols: message.Resize.Cols,
						Rows: message.Resize.Rows,
					}); err != nil {
						return
					}
					announced = *message.Resize
				}
				if err := writeSeed(ctx, conn, message.Resync); err != nil {
					return
				}
				continue
			}
			if ok && message.Resize != nil {
				// A terminal this viewer shares can be moved by anyone
				// holding it — the dashboard, a full-screen attach — and
				// a replica painting at a size nobody else is using is a
				// diverged replica. The notice travels just ahead of the
				// repaint that belongs to it.
				if err := writeControl(ctx, conn, controlMessage{
					Type: controlResize,
					Cols: message.Resize.Cols,
					Rows: message.Resize.Rows,
				}); err != nil {
					return
				}
				continue
			}
			if !ok {
				// The stream ended — and this layer cannot say why. The
				// agent may have exited, or the attachment may have ended
				// some other way. Saying "session ended" here would have
				// a live agent read as a dead one; the client reconnects
				// and asks the roster, which knows. Falling behind is no
				// longer among the reasons: this attachment resyncs.
				_ = conn.Close(websocket.StatusNormalClosure, "stream ended")
				return
			}
			if err := conn.Write(ctx, websocket.MessageBinary, message.Bytes); err != nil {
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
			// Refuse a size rather than correct it. Clamping upward looks
			// harmless and is not: a message with no size at all would
			// become a legal 2x2, and one resize reflows the terminal
			// every viewer shares — including the dashboard someone is
			// reading. An unusable number is a client bug; the terminal
			// is not the place to absorb it.
			if !usableSize(message.Cols, message.Rows) {
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

// keepaliveInterval paces the ping that proves a viewer is still there.
// A laptop that sleeps or a wifi drop leaves a connection that looks open
// and never speaks again; without this the server holds the goroutine and
// the daemon attachment behind it until the OS gives up on the socket,
// which can be hours.
const keepaliveInterval = 30 * time.Second

func keepalive(ctx context.Context, cancel context.CancelFunc, conn *websocket.Conn) {
	defer cancel()
	ticker := time.NewTicker(keepaliveInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		pingCtx, timeout := context.WithTimeout(ctx, keepaliveInterval)
		err := conn.Ping(pingCtx)
		timeout()
		if err != nil {
			return
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

// A terminal's geometry is not a matter of opinion, and this is the one
// place a client's number reaches the daemon's emulator. The emulator
// allocates the whole grid — screen and scrollback both — so an unbounded
// size is an out-of-memory request served on demand, and the daemon that
// serves it owns every agent's process.
//
// The bounds are sized against real displays with room to spare, not
// against what the format allows: an ultrawide monitor at a small font
// reaches perhaps 400 columns, and rows past a couple of hundred are a
// scrollback question rather than a screen one. Generous limits are not
// free here — scrollback is columns times its line count, so width is
// what actually costs, and a cap ten times any real terminal would mean
// hundreds of megabytes per session for no one's benefit.
const (
	maxTerminalCols = 500
	maxTerminalRows = 200
)

// usableSize reports whether a client's geometry is one a terminal can
// actually be.
func usableSize(cols, rows int) bool {
	return cols >= 2 && rows >= 2 && cols <= maxTerminalCols && rows <= maxTerminalRows
}

// maxTerminalMessage bounds one inbound message — a keystroke, or a
// paste. Generous enough for pasting a file, small enough that a client
// cannot make the server hold an arbitrary buffer.
const maxTerminalMessage = 4 << 20

// terminalSize reads the viewer's geometry from the query string. A
// viewer that does not say — or says something a terminal cannot be —
// gets a conventional one; it will resize as soon as it has laid itself
// out. Unlike the resize message, there is no client to inform here, and
// refusing the whole attachment over a query string would be a worse
// answer than starting at 80x24.
func terminalSize(r *http.Request) (cols, rows int) {
	cols, rows = queryInt(r, "cols"), queryInt(r, "rows")
	if !usableSize(cols, rows) {
		return 80, 24
	}
	return cols, rows
}

func queryInt(r *http.Request, name string) int {
	value, err := strconv.Atoi(r.URL.Query().Get(name))
	if err != nil {
		return 0
	}
	return value
}
