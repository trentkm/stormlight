// Package remote reaches a Stormlight host that is not this one.
//
// The daemon runs on the machine the agents run on — that is the whole
// design. An agent's PTY, its provider process, its hooks, its transcript
// file, and the repository it is working in all live together; what
// crosses the network is the windrunner wire protocol and nothing else.
// A remote host is therefore not a different kind of runtime. It is the
// same runtime over a different transport: `ssh <host> stormlight
// _wrbridge`, which ensures a daemon on that host and then splices its
// own stdio onto that daemon's socket.
//
// The trust boundary comes along unchanged. The wire protocol has no
// authentication of its own and does not need any: reaching the socket
// means being the user who owns it, enforced by directory permissions
// locally and by the host's own login remotely. The bridge runs as that
// user. Nothing binds a port.
package remote

// Protocol is the bridge contract this build speaks. It changes only when
// the framing around the tunnel changes — a new handshake field that
// matters, a different splice — never for an ordinary release, so hosts
// on adjacent versions keep working.
//
// 2: a remote dispatch launches the provider through the host's login
// shell, which reaches the provider by way of `stormlight _exec` on that
// machine. A binary without that subcommand cannot serve a dispatch, and
// nothing else in the greeting reveals it — so an older host has to be
// refused here, where the failure can name both versions and the way
// out, rather than per dispatch as an agent that dies complaining about
// a directory.
const Protocol = 2

// Hello is the one line a bridge writes before it becomes a byte pipe.
// It answers the questions dispatch would otherwise have to guess at from
// the local machine, where every answer is wrong: where the remote
// binary lives (hooks re-invoke it through $STORMLIGHT_BIN), which socket
// directory its daemon is on (a hook's own event call has to reach the
// same one), and whether the two ends can talk at all.
type Hello struct {
	Protocol  int    `json:"protocol"`
	Version   string `json:"version"`
	Bin       string `json:"bin"`
	SocketDir string `json:"socket_dir"`
	Hostname  string `json:"hostname"`
	// DaemonCapability is what the daemon actually serving this socket
	// can be asked to do — which is not what Version says, and is the
	// difference that matters.
	//
	// The bridge is a fresh process of a known build, but the daemon
	// behind it is whatever was already listening, started by whatever
	// binary was installed at the time and outliving every upgrade
	// since. Version describes the messenger. This describes the
	// machine that will actually run the agent.
	//
	// Zero means the daemon predates saying so, which is an answer:
	// nothing older can be asked, and nothing older honours the fields a
	// dispatch depends on.
	DaemonCapability int `json:"daemon_capability,omitempty"`
	// DaemonVersion is the build that started that daemon, for a message
	// that can say how far behind it is.
	DaemonVersion string `json:"daemon_version,omitempty"`
	// DaemonPID names the process to end, since ending it is the only
	// way to replace it and there is no gentler instruction to give.
	DaemonPID int `json:"daemon_pid,omitempty"`
}
