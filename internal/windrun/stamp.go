package windrun

import (
	"encoding/json"
	"os"
	"path/filepath"
	"syscall"
)

// A daemon outlives the binary that started it. That is the point of it —
// agents survive the dashboard, the terminal, and the upgrade — and it is
// also the one way a machine can be running code nobody chose.
//
// The gap it opens is not about versions but about capability. A dispatch
// asks the daemon to do particular things with a spawn request, and a
// daemon that predates one of them does not refuse: it drops the field
// and starts the process anyway. What arrives is an agent missing the
// environment its whole lifecycle depends on, reporting a failure three
// layers from the cause — see #144, where a daemon two releases old cost
// an afternoon and looked like two unrelated bugs.
//
// So the daemon says what it is when it starts, and the bridge reads that
// rather than assuming the binary beside it is the one serving.

// Capability is what a daemon started by this build can be asked to do.
// It is not the version: two releases apart with nothing between them
// that the daemon must honour differently share a capability, and only a
// change in what a spawn request may contain moves it.
//
// 1: EnvOverride — the field a remote dispatch puts the agent's identity,
// its binary, and its socket directory in. Since #188 the provider's argv
// travels there too, so a daemon that drops it does not merely leave an
// agent unable to report state; it leaves one that cannot start.
const Capability = 1

// Stamp is what a running daemon says about itself, left beside its
// socket for whatever connects to it.
type Stamp struct {
	Capability int    `json:"capability"`
	Version    string `json:"version"`
	// PID is what makes the file evidence rather than a leftover: a
	// daemon that died without cleaning up leaves a stamp that names a
	// process no longer there, and an unowned stamp is worth exactly as
	// much as no stamp at all.
	PID int `json:"pid"`
}

// StampPath is where a daemon's stamp lives: beside its socket, because
// what it describes is that socket rather than that machine, and a
// scratch WINDRUNNER_DIR gets its own.
func StampPath() string {
	return filepath.Join(SocketDir(), "daemon.json")
}

// WriteStamp records what this daemon can do, before it can be connected
// to. Written first and removed on failure rather than written after
// listening: something that connects the instant the socket appears must
// not find the socket without the stamp and conclude the daemon is old.
func WriteStamp(version string) error {
	encoded, err := json.Marshal(Stamp{
		Capability: Capability,
		Version:    version,
		PID:        os.Getpid(),
	})
	if err != nil {
		return err
	}
	if err := os.MkdirAll(SocketDir(), 0o700); err != nil {
		return err
	}
	return os.WriteFile(StampPath(), encoded, 0o600)
}

// RemoveStamp clears the record when the daemon goes, so that the next
// thing to look finds nothing rather than a claim about a dead process.
func RemoveStamp() { os.Remove(StampPath()) }

// ReadStamp is what the daemon behind this socket says about itself.
//
// Absence is an answer, and the important one: a daemon started before
// stamps existed leaves none, which is precisely the daemon whose
// capabilities cannot be asked about any other way.
func ReadStamp() (Stamp, bool) {
	raw, err := os.ReadFile(StampPath())
	if err != nil {
		return Stamp{}, false
	}
	var stamp Stamp
	if err := json.Unmarshal(raw, &stamp); err != nil {
		return Stamp{}, false
	}
	if !running(stamp.PID) {
		return Stamp{}, false
	}
	return stamp, true
}

// running reports whether a process is there to be signalled. Signal 0 is
// the ask-without-telling: it performs the permission and existence
// checks and delivers nothing.
func running(pid int) bool {
	if pid <= 0 {
		return false
	}
	process, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return process.Signal(syscall.Signal(0)) == nil
}
