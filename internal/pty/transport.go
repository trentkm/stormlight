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
