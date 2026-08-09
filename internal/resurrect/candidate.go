package resurrect

// Candidate is a snapshot entry weighed against the world as it is now: the
// providers installed, the directories that still exist, the agents already
// running. It is what both the dashboard and `stormlight restore` read.
//
// An entry that cannot come back keeps its place in the list with the reason
// attached, rather than being filtered out. A roster that quietly loses rows
// is how a human concludes an agent never existed.
type Candidate struct {
	Entry Entry
	// Live is true when an agent with this id is already running — the
	// snapshot and the runtime agree, and there is nothing to restore.
	Live bool
	// Reason is why this entry cannot be restored; empty means it can.
	Reason string
}

// Restorable reports whether restoring this candidate would do anything.
func (c Candidate) Restorable() bool {
	return !c.Live && c.Reason == ""
}

// Restorable filters candidates down to the ones a restore would act on.
func Restorable(candidates []Candidate) []Candidate {
	var out []Candidate
	for _, candidate := range candidates {
		if candidate.Restorable() {
			out = append(out, candidate)
		}
	}
	return out
}
