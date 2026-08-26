package app

import (
	"context"
	"errors"
	"time"

	"github.com/trentkm/stormlight/internal/agent"
	"github.com/trentkm/stormlight/internal/diagnostic"
	"github.com/trentkm/stormlight/internal/link"
)

// Links, the pipeline over the fleet. The rules live in internal/link;
// this is delivery: resolving the agents at each end, reading what the
// source last said, and writing the hop into the target's terminal —
// or parking it, when the target is mid-turn.
//
// Every entry point runs under the store's lock, including the send
// itself. The writers are separate processes (each provider hook is
// one), and a hop that read the state, sent, and then wrote would race
// another hook's read in between; holding the lock across the send
// costs a few milliseconds of serialisation and buys one order of
// events.

// Links is every link, never nil.
func (s *Service) Links(context.Context) ([]link.Link, error) {
	state, err := s.links.Load()
	if err != nil {
		return nil, err
	}
	if state.Links == nil {
		return []link.Link{}, nil
	}
	return state.Links, nil
}

// AddLink draws an arrow between two agents that exist. Ids may be
// prefixes, the way every agent command takes them; what is stored is
// the full id, since a prefix is only unique today.
func (s *Service) AddLink(
	ctx context.Context,
	from, to, label string,
	auto bool,
) (link.Link, error) {
	source, err := s.find(ctx, from)
	if err != nil {
		return link.Link{}, err
	}
	target, err := s.find(ctx, to)
	if err != nil {
		return link.Link{}, err
	}
	var added link.Link
	err = s.links.Mutate(func(state *link.State) error {
		added, err = state.Add(source.ID, target.ID, label, auto)
		return err
	})
	return added, err
}

func (s *Service) UpdateLink(
	_ context.Context,
	id string,
	patch link.Patch,
) (link.Link, error) {
	var updated link.Link
	err := s.links.Mutate(func(state *link.State) error {
		var err error
		updated, err = state.Update(id, patch)
		return err
	})
	return updated, err
}

func (s *Service) RemoveLink(_ context.Context, id string) error {
	return s.links.Mutate(func(state *link.State) error {
		return state.Remove(id)
	})
}

// FireLink sends a hop by hand: the link's label and the source's last
// reply, at hop zero, since a human's hand is where a chain starts.
func (s *Service) FireLink(ctx context.Context, id string) (link.Link, error) {
	var fired link.Link
	err := s.links.Mutate(func(state *link.State) error {
		found, err := state.Find(id)
		if err != nil {
			return err
		}
		source, err := s.find(ctx, found.From)
		if err != nil {
			return err
		}
		if source.LastReply == "" {
			return link.ErrNothingToSend
		}
		message := link.Compose(found.Label, displayName(source), source.LastReply)
		if err := s.deliver(ctx, state, found, message, 0); err != nil {
			return err
		}
		fired = *found
		return nil
	})
	return fired, err
}

// TurnEnded is the engine's tick: the provider reported that an agent's
// turn is over. Every automatic link leaving it fires — unless the turn
// was itself the last hop a chain may take unattended — and every link
// arriving at it that was waiting for it to go idle delivers now.
//
// Called by the provider hook after the turn-end update has landed, so
// the agent's activity and last reply are the ones this turn produced.
func (s *Service) TurnEnded(ctx context.Context, agentID string) error {
	return s.links.Mutate(func(state *link.State) error {
		source, err := s.find(ctx, agentID)
		if err != nil {
			return err
		}
		hop := state.TakeInbound(source.ID)
		for _, outgoing := range state.From(source.ID) {
			if !outgoing.Auto {
				continue
			}
			if hop >= link.HopLimit {
				diagnostic.Logger().Info("link chain stopped at the hop limit",
					"link", outgoing.ID, "from", source.ID, "hop", hop)
				continue
			}
			if source.LastReply == "" {
				continue
			}
			message := link.Compose(outgoing.Label, displayName(source), source.LastReply)
			if err := s.deliver(ctx, state, outgoing, message, hop); err != nil {
				diagnostic.Logger().Warn("link failed to fire",
					"link", outgoing.ID, "error", err)
			}
		}
		for _, incoming := range state.To(source.ID) {
			if incoming.Pending == nil {
				continue
			}
			pending := *incoming.Pending
			if err := s.deliver(ctx, state, incoming, pending.Message, pending.Hop); err != nil {
				diagnostic.Logger().Warn("pending link failed to deliver",
					"link", incoming.ID, "error", err)
			}
		}
		return nil
	})
}

// deliver writes a hop into its target, attributed to the source's
// session so the daemon's audit trail says who spoke — or parks it on
// the link when the target is mid-turn, for the target's own turn end
// to deliver. A message that lands in an input box mid-turn would be
// submitted the moment the turn ended, which is the same thing done
// less legibly.
func (s *Service) deliver(
	ctx context.Context,
	state *link.State,
	l *link.Link,
	message string,
	hop int,
) error {
	target, err := s.find(ctx, l.To)
	if err != nil {
		return err
	}
	if target.Activity == agent.ActivityWorking || target.Activity == agent.ActivityStarting {
		l.Pending = &link.Pending{Message: message, Hop: hop, At: time.Now()}
		return nil
	}
	source, err := s.find(ctx, l.From)
	if err != nil {
		return err
	}
	if err := s.runtime.Send(ctx, target.ID, message, source.PaneID); err != nil {
		return err
	}
	state.Delivered(l, hop, time.Now())
	return nil
}

// forgetLinks drops an agent's links when the agent goes; a link to
// nowhere would be an error every turn end.
func (s *Service) forgetLinks(agentID string) {
	err := s.links.Mutate(func(state *link.State) error {
		state.RemoveAgent(agentID)
		return nil
	})
	if err != nil && !errors.Is(err, context.Canceled) {
		diagnostic.Logger().Warn("forget links", "agent", agentID, "error", err)
	}
}

func displayName(managedAgent agent.Agent) string {
	switch {
	case managedAgent.Name != "":
		return managedAgent.Name
	case managedAgent.Task != "":
		return managedAgent.Task
	default:
		return managedAgent.ID
	}
}
