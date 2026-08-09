package app

// Keeping the agent roster on disk, and building it back from there.
// See #91 and internal/resurrect.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/trentkm/stormlight/internal/agent"
	"github.com/trentkm/stormlight/internal/diagnostic"
	"github.com/trentkm/stormlight/internal/resurrect"
	"github.com/trentkm/stormlight/internal/session"
)

// RestoreResult is what became of one entry a restore was asked to bring
// back. Failures are reported per entry rather than aborting the run: one
// agent whose worktree was torn down is no reason to abandon the other nine.
type RestoreResult struct {
	Entry resurrect.Entry
	Agent agent.Agent
	Err   error
}

// SnapshotPath names the file the roster is recorded in.
func (s *Service) SnapshotPath() string {
	if s.store == nil {
		return ""
	}
	return s.store.Path()
}

// restorer reports the runtime's restore capability, if it has one. A
// runtime that cannot lose its roster has nothing to record and nothing to
// build back.
func (s *Service) restorer() (session.Restorer, bool) {
	if s.store == nil {
		return nil, false
	}
	restorer, ok := s.runtime.(session.Restorer)
	return restorer, ok
}

// snapshot records the roster, but only when the runtime says its listing is
// authoritative. An empty listing from a runtime whose session is gone means
// "everything was lost", and writing that as "there are no agents" would
// destroy the record at the one moment it matters.
func (s *Service) snapshot(ctx context.Context, agents []agent.Agent) {
	restorer, ok := s.restorer()
	if !ok {
		return
	}
	live, err := restorer.RosterLive(ctx)
	if err != nil {
		diagnostic.Logger().Debug("roster authority unknown", "error", err)
		return
	}
	if !live {
		return
	}
	if err := s.store.Save(restorer.SessionName(), agents); err != nil {
		diagnostic.Logger().Warn("agent snapshot not saved", "error", err)
	}
}

// resnapshot re-reads the roster and records it. State changes arrive from
// provider hooks in processes that never list anything, so the listing the
// dashboard would eventually do is not one to wait for — an agent that
// started waiting for an answer at 3am should be waiting for it after the
// reboot too.
func (s *Service) resnapshot(ctx context.Context) {
	if _, ok := s.restorer(); !ok {
		return
	}
	// Deliberately the service's own listing rather than the runtime's: it is
	// the one that backfills workspaces and overlays catalog display names, and
	// a record written from the barer listing would differ from the one the
	// dashboard's poll writes — leaving the two paths rewriting the file past
	// each other forever.
	if _, err := s.ListAgents(ctx); err != nil {
		diagnostic.Logger().Debug("roster unavailable for snapshot", "error", err)
	}
}

// RestoreCandidates weighs every remembered agent against the world as it is
// now: which are already running, which providers can reopen a conversation,
// which transcripts and directories still exist.
//
// Entries that cannot come back keep their place in the list with the reason
// attached. A restore screen that quietly drops rows is how a human concludes
// an agent never existed.
func (s *Service) RestoreCandidates(
	ctx context.Context,
) ([]resurrect.Candidate, error) {
	if s.store == nil {
		return nil, nil
	}
	snapshot, err := s.store.Load()
	if err != nil {
		return nil, err
	}
	if len(snapshot.Agents) == 0 {
		return nil, nil
	}

	live := map[string]bool{}
	if agents, listErr := s.runtime.ListAgents(ctx); listErr == nil {
		for _, managedAgent := range agents {
			live[managedAgent.ID] = true
		}
	}

	_, restorable := s.restorer()
	candidates := make([]resurrect.Candidate, 0, len(snapshot.Agents))
	for _, entry := range snapshot.Agents {
		candidates = append(candidates, resurrect.Candidate{
			Entry:  entry,
			Live:   live[entry.ID],
			Reason: s.restoreObstacle(entry, restorable),
		})
	}
	return candidates, nil
}

// entrySessionID resolves the conversation id an entry holds: the recorded
// session id when the agent's hooks reported one, else whatever the
// provider's transcript naming gives back — the fallback that keeps entries
// recorded before session-id capture restorable.
func (s *Service) entrySessionID(entry resurrect.Entry) string {
	return s.providers.SessionID(
		entry.Provider,
		entry.SessionID,
		entry.TranscriptPath,
	)
}

// restoreObstacle names why an entry cannot be brought back, or returns an
// empty string if it can.
func (s *Service) restoreObstacle(entry resurrect.Entry, restorable bool) string {
	if !restorable {
		return "this runtime cannot restore agents"
	}
	if !s.providers.CanResume(entry.Provider) {
		return fmt.Sprintf(
			"%s cannot reopen a conversation",
			s.providers.Label(entry.Provider),
		)
	}
	if strings.TrimSpace(entry.TranscriptPath) == "" {
		return "no transcript: the agent never reported a turn"
	}
	if _, err := os.Stat(entry.TranscriptPath); err != nil {
		return "the transcript is gone"
	}
	if s.entrySessionID(entry) == "" {
		return "the transcript does not name a conversation"
	}
	info, err := os.Stat(entry.Cwd)
	if err != nil || !info.IsDir() {
		return "the working directory is gone"
	}
	return ""
}

// Restore brings back the named agents, or every restorable one when no id is
// named. Ids may be shortened the way every other command accepts them.
func (s *Service) Restore(ctx context.Context, ids ...string) ([]RestoreResult, error) {
	candidates, err := s.RestoreCandidates(ctx)
	if err != nil {
		return nil, err
	}
	chosen, err := selectCandidates(candidates, ids)
	if err != nil {
		return nil, err
	}
	results := make([]RestoreResult, 0, len(chosen))
	for _, candidate := range chosen {
		restored, restoreErr := s.restoreOne(ctx, candidate.Entry)
		results = append(results, RestoreResult{
			Entry: candidate.Entry,
			Agent: restored,
			Err:   restoreErr,
		})
	}
	s.resnapshot(ctx)
	return results, nil
}

func (s *Service) restoreOne(
	ctx context.Context,
	entry resurrect.Entry,
) (agent.Agent, error) {
	restorer, ok := s.restorer()
	if !ok {
		return agent.Agent{}, errors.New("this runtime cannot restore agents")
	}
	sessionID := s.entrySessionID(entry)
	if sessionID == "" {
		return agent.Agent{}, fmt.Errorf(
			"%s transcript %q does not name a conversation",
			s.providers.Label(entry.Provider),
			entry.TranscriptPath,
		)
	}
	launch, err := s.providers.Resume(entry.Provider, sessionID, entry.Mode)
	if err != nil {
		return agent.Agent{}, err
	}
	restored, err := restorer.Restore(ctx, session.RestoreRequest{
		Agent: agent.Agent{
			ID:             entry.ID,
			Provider:       entry.Provider,
			Name:           entry.Name,
			Task:           entry.Task,
			Summary:        entry.Summary,
			Cwd:            entry.Cwd,
			CreatedAt:      entry.CreatedAt,
			Attention:      entry.Attention,
			AttentionAt:    entry.AttentionAt,
			Mark:           entry.Mark,
			Mode:           entry.Mode,
			SessionID:      sessionID,
			TranscriptPath: entry.TranscriptPath,
			Workspace:      entry.Workspace,
		},
		Launch: launch,
	})
	if err != nil {
		return agent.Agent{}, err
	}
	diagnostic.Logger().Info("agent restored",
		"agent_id", restored.ID,
		"provider", restored.Provider,
		"window_id", restored.WindowID,
	)
	return restored, nil
}

// Forget drops agents from the record without touching anything running.
// It is how a human closes the book on an agent whose window died with its
// server and whose conversation they do not want back.
func (s *Service) Forget(_ context.Context, ids ...string) error {
	if s.store == nil || len(ids) == 0 {
		return nil
	}
	snapshot, err := s.store.Load()
	if err != nil {
		return err
	}
	resolved := make([]string, 0, len(ids))
	for _, id := range ids {
		entry, found := snapshot.Find(id)
		if !found {
			return fmt.Errorf("no remembered agent %q", id)
		}
		resolved = append(resolved, entry.ID)
	}
	return s.store.Forget(resolved...)
}

// selectCandidates resolves the ids a restore names, defaulting to every
// entry that can come back. Naming an entry that cannot is an error rather
// than a silent skip: the human asked for that agent specifically and
// deserves to hear why they are not getting it.
func selectCandidates(
	candidates []resurrect.Candidate,
	ids []string,
) ([]resurrect.Candidate, error) {
	if len(ids) == 0 {
		return resurrect.Restorable(candidates), nil
	}
	chosen := make([]resurrect.Candidate, 0, len(ids))
	for _, id := range ids {
		index := slices.IndexFunc(candidates, func(c resurrect.Candidate) bool {
			return c.Entry.ID == id
		})
		if index < 0 {
			matches := 0
			for position, candidate := range candidates {
				if strings.HasPrefix(candidate.Entry.ID, id) {
					index, matches = position, matches+1
				}
			}
			if matches > 1 {
				return nil, fmt.Errorf("agent id %q is ambiguous", id)
			}
		}
		if index < 0 {
			return nil, fmt.Errorf("no remembered agent %q", id)
		}
		candidate := candidates[index]
		switch {
		case candidate.Live:
			return nil, fmt.Errorf("agent %s is already running", candidate.Entry.ID)
		case candidate.Reason != "":
			return nil, fmt.Errorf(
				"agent %s cannot be restored: %s",
				candidate.Entry.ID,
				candidate.Reason,
			)
		}
		chosen = append(chosen, candidate)
	}
	return chosen, nil
}
