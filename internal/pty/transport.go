package pty

import "context"

// Transport supplies a terminal snapshot, its live stream, and I/O.
type Transport interface {
	Seed() []byte
	Output() <-chan Message
	Write([]byte) error
	Resize(context.Context, int, int) error
	Close()
}

// Message is one delivery on a terminal's stream. Exactly one of its
// fields is set, and they arrive in the order the daemon sent them.
//
// One ordered stream rather than a channel of bytes plus callbacks for
// the rest, deliberately. A resync says "replace your replica with this"
// and output says "append this", so delivering them by two routes lets a
// slow reader — the only kind that ever gets a resync — apply queued
// output on top of the state that superseded it. The replica is then
// wrong from that point on, with nothing following to correct it. The
// same argument retired the resize callback: a size that arrives out of
// band is a size applied to the wrong bytes.
type Message struct {
	// Bytes is terminal output to append to the replica.
	Bytes []byte
	// Resize is the hosted terminal's new size. The terminal is shared,
	// so it moves when anyone moves it — another dashboard, a browser, a
	// full-screen attach — and the repaint rides the stream just behind
	// this notice.
	Resize *Size
	// Resync replaces the replica wholesale: scrollback, screen, cursor.
	// It arrives when this viewer fell far enough behind that the daemon
	// sent exact state instead of the bytes it missed, so a consumer must
	// reset its emulator and replay this rather than writing it into the
	// one it already has — which would double the scrollback it carries.
	Resync []byte
}

// Size is a terminal's dimensions.
type Size struct {
	Cols, Rows int
}
