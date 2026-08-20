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
	// never appends a screen to the one it was already showing — and send
	// the size it was rendered at first, because a client that named no
	// geometry of its own has no other way to learn what these bytes were
	// wrapped for.
	if err := writeState(ctx, conn, transport.Seed()); err != nil {
		return
	}

	go s.pumpInput(ctx, cancel, conn, transport)
	go keepalive(ctx, cancel, conn)

	// Output goes out in bursts rather than in reads.
	//
	// A repaint is not one read. The PTY hands back whatever its buffer
	// held when the reader got there, so a program that erases the
	// screen and redraws it in a single breath still arrives split —
	// measured on this socket, the message carrying codex's ESC[2J and
	// the message carrying the redraw behind it land a tenth of a
	// millisecond apart. Relayed separately, a viewer can paint between
	// them, and what it paints is the erased screen: the pane blanks and
	// comes back.
	//
	// Nothing downstream can repair that. A client writes a message into
	// its replica indivisibly, so bytes that share a message are painted
	// together and bytes that do not, may not be. The atomicity has to
	// be built here, where the pieces are still adjacent: everything
	// already queued leaves together, and a brief pause lets the rest of
	// a repaint catch up. It is what a local terminal gets for free by
	// draining its fd before it renders.
	for {
		select {
		case <-ctx.Done():
			return
		case message, ok := <-transport.Output():
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
			for pending := &message; pending != nil; {
				next, err := relay(ctx, conn, transport.Output(), *pending)
				if err != nil {
					return
				}
				pending = next
			}
		}
	}
}

// relay writes one delivery and returns whatever ended it, so the caller
// can write that in turn. Ordering is the contract this whole plane rests
// on, and this is where it is kept: state and sizes go out on their own,
// output goes out as the burst it belongs to, and a notice that arrived
// mid-burst waits for the bytes in front of it.
func relay(
	ctx context.Context,
	conn *websocket.Conn,
	output <-chan pty.Message,
	message pty.Message,
) (*pty.Message, error) {
	switch {
	case message.Resync != nil:
		// State first: a resync carries its own size, and the resize
		// branch below would otherwise swallow it.
		//
		// This viewer fell behind and the daemon sent state instead of
		// the bytes it missed — the same shape as the opening seed, and
		// written by the same function, because they are the same thing
		// at different moments.
		return nil, writeState(ctx, conn, message)
	case message.Resize != nil:
		// A terminal this viewer shares can be moved by anyone holding
		// it — the dashboard, a full-screen attach — and a replica
		// painting at a size nobody else is using is a diverged replica.
		// The notice travels just ahead of the repaint that belongs to
		// it.
		return nil, writeControl(ctx, conn, controlMessage{
			Type: controlResize,
			Cols: message.Resize.Cols,
			Rows: message.Resize.Rows,
		})
	}
	burst, next := gatherBurst(ctx, output, message.Bytes)
	if len(burst) == 0 {
		return next, nil
	}
	if err := conn.Write(ctx, websocket.MessageBinary, burst); err != nil {
		return nil, err
	}
	return next, nil
}

const (
	// burstQuiet is how long output waits for the rest of its repaint.
	// The pieces of one arrive a fraction of a millisecond apart, so this
	// is far longer than the gap inside a repaint and far shorter than
	// the gap a person can see.
	burstQuiet = 3 * time.Millisecond
	// burstCap bounds that wait, so a stream that never falls quiet
	// reaches the viewer on time rather than being held for a pause that
	// is not coming.
	burstCap = 16 * time.Millisecond
	// burstBytes bounds one message. A repaint is kilobytes; this is the
	// backstop for a program dumping a file, where atomicity buys nothing
	// and holding megabytes costs.
	burstBytes = 256 << 10
)

// gatherBurst collects consecutive output into one message, stopping at
// the first pause, at the cap, or at a notice that has to be written on
// its own — which it hands back rather than swallowing.
func gatherBurst(
	ctx context.Context,
	output <-chan pty.Message,
	first []byte,
) (burst []byte, next *pty.Message) {
	burst = append(make([]byte, 0, len(first)), first...)
	quiet := time.NewTimer(burstQuiet)
	defer quiet.Stop()
	deadline := time.NewTimer(burstCap)
	defer deadline.Stop()
	for len(burst) < burstBytes {
		select {
		case <-ctx.Done():
			return burst, nil
		case message, ok := <-output:
			if !ok {
				return burst, nil
			}
			if message.Resync != nil || message.Resize != nil {
				held := message
				return burst, &held
			}
			burst = append(burst, message.Bytes...)
			// Stopped before reset, and drained if it had already fired:
			// a timer reset with its value still in the channel fires at
			// once, which would end every burst at its first chunk and
			// put this right back where it started.
			if !quiet.Stop() {
				<-quiet.C
			}
			quiet.Reset(burstQuiet)
		case <-quiet.C:
			return burst, nil
		case <-deadline.C:
			return burst, nil
		}
	}
	return burst, nil
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

// writeState sends terminal state — the attach seed, or a resync — as the
// size it was rendered at, then the notice, then the bytes.
//
// The size leads because the client has to size the replica it is about
// to fill; the notice is what tells it these bytes replace rather than
// extend.
func writeState(ctx context.Context, conn *websocket.Conn, state pty.Message) error {
	if state.Resize != nil {
		if err := writeControl(ctx, conn, controlMessage{
			Type: controlResize,
			Cols: state.Resize.Cols,
			Rows: state.Resize.Rows,
		}); err != nil {
			return err
		}
	}
	if err := writeControl(ctx, conn, controlMessage{Type: controlSeed}); err != nil {
		return err
	}
	return conn.Write(ctx, websocket.MessageBinary, state.Resync)
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

// terminalSize reads the viewer's geometry from the query string, or
// zero when it names none.
//
// Zero means "no opinion", and it is not the same as a default. The
// terminal is shared, so any size this returns is asserted on the agent
// for every viewer — a browser tab that has not laid itself out yet would
// otherwise reflow the dashboard's pane to 80x24 and leave it there,
// because nothing re-asserts the dashboard's own size afterward. A viewer
// that cannot say has nothing to say; it will resize as soon as it has a
// shape (see #155).
func terminalSize(r *http.Request) (cols, rows int) {
	cols, rows = queryInt(r, "cols"), queryInt(r, "rows")
	if !usableSize(cols, rows) {
		return 0, 0
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
