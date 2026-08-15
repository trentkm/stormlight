package pty

import "context"

// Transport supplies a terminal snapshot, its live byte stream, and I/O.
type Transport interface {
	Seed() []byte
	Output() <-chan []byte
	Write([]byte) error
	Resize(context.Context, int, int) error
	Close()
}

// ResizeNotifier is the optional transport capability behind a shared
// terminal: the hosted terminal can be moved by someone else — another
// dashboard, a full-screen attach — and a transport that can say so lets
// the widget's emulator follow the terminal's true size instead of
// painting a diverged replica.
type ResizeNotifier interface {
	OnResize(handler func(cols, rows int))
}
