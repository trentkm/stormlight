// Package link is the pipeline over the fleet: arrows between agents that
// send.
//
// A link from A to B says that when A's turn ends, B receives the link's
// label and what A just said — the label is the instruction for the hop,
// and A's last reply is the material. Draw implementer → reviewer →
// implementer with labels "review this diff" and "address the review"
// and the loop runs itself, each turn end firing the next hop, whether or
// not any dashboard is open to watch it.
//
// This package is the state and the rules: what a link is, which links
// may exist (no self-links, no cycles — a static loop would run without
// a human in it), what a hop's message looks like, and how far a chain
// of automatic hops may run before a human has to touch it. Delivery —
// finding the agents, writing into the target's terminal — is the
// service's, which owns the runtime; the rules here never touch one.
package link

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/trentkm/stormlight/internal/filelock"
)

// HopLimit is how many automatic hops a chain may take from the last
// message a human sent. Cycle refusal at creation catches the static
// loops; this catches the ones that route through a person — a reviewer
// whose verdict is answered by the implementer, whose fix is reviewed
// again — so that they stop and ask rather than run all night.
const HopLimit = 3

// LabelLimit bounds a link's label. It is a prompt, so it is generous;
// it is stored and pushed with every roster, so it is bounded.
const LabelLimit = 2000

// ReplyLimit bounds what a hop carries of the source's last reply, in
// runes. A whole reply is what the next agent needs; a whole pasted file
// inside one is not.
const ReplyLimit = 12000

var (
	ErrSelf          = errors.New("a link cannot go from an agent to itself")
	ErrCycle         = errors.New("that link would close a loop")
	ErrNotFound      = errors.New("no such link")
	ErrNothingToSend = errors.New("the source has not said anything yet")
	ErrHopLimit      = fmt.Errorf("the chain has run %d hops without a human; it stops here", HopLimit)
)

// A Link is one arrow: from an agent, to an agent, carrying a label.
type Link struct {
	ID    string `json:"id"`
	From  string `json:"from"`
	To    string `json:"to"`
	Label string `json:"label"`
	// Auto fires the link when the source's turn ends. Off, the link is
	// a route fired by hand.
	Auto bool `json:"auto"`
	// LastFired is when the link last delivered, for the canvas to show.
	LastFired time.Time `json:"last_fired,omitzero"`
	// Pending is a message that could not be delivered because the
	// target was mid-turn; the target's own turn end delivers it. One
	// per link: a newer one replaces an older, since the older was
	// about a reply the source has since superseded.
	Pending *Pending `json:"pending,omitempty"`
}

// A Pending message waits for its target to go idle.
type Pending struct {
	Message string    `json:"message"`
	Hop     int       `json:"hop"`
	At      time.Time `json:"at"`
}

// Inbound records that an agent's current turn was started by a link,
// and at which hop, so the hops it fires in turn count from there. It is
// consumed when the agent's turn ends.
type Inbound struct {
	Link string    `json:"link"`
	Hop  int       `json:"hop"`
	At   time.Time `json:"at"`
}

// State is everything the store holds.
type State struct {
	Links   []Link             `json:"links"`
	Inbound map[string]Inbound `json:"inbound,omitempty"`
}

// Patch is what an existing link may change: its label, its auto flag.
// Its ends are fixed — a link between different agents is a different
// link, and re-pointing one would skip the cycle check.
type Patch struct {
	Label *string
	Auto  *bool
}

// Compose is the message a hop delivers: the label, then who is speaking
// and what they said. The label first, because it is the instruction and
// the reply is its material; the attribution, because the target has no
// other way to know this was not its human.
func Compose(label, fromName, reply string) string {
	reply = strings.TrimSpace(reply)
	if runes := []rune(reply); len(runes) > ReplyLimit {
		reply = string(runes[:ReplyLimit]) + "…"
	}
	var b strings.Builder
	if label = strings.TrimSpace(label); label != "" {
		b.WriteString(label)
		b.WriteString("\n\n")
	}
	fmt.Fprintf(&b, "From %s:\n%s", fromName, reply)
	return b.String()
}

// Reaches reports whether following links from one agent arrives at
// another. Adding a link from A to B is refused when B already reaches
// A: the new link would close the loop.
func Reaches(links []Link, from, to string) bool {
	seen := map[string]bool{}
	frontier := []string{from}
	for len(frontier) > 0 {
		at := frontier[len(frontier)-1]
		frontier = frontier[:len(frontier)-1]
		if at == to {
			return true
		}
		if seen[at] {
			continue
		}
		seen[at] = true
		for _, link := range links {
			if link.From == at {
				frontier = append(frontier, link.To)
			}
		}
	}
	return false
}

// Add appends a link, refusing one that goes nowhere or closes a loop.
func (s *State) Add(from, to, label string, auto bool) (Link, error) {
	if from == "" || to == "" {
		return Link{}, errors.New("a link needs both ends")
	}
	if from == to {
		return Link{}, ErrSelf
	}
	if Reaches(s.Links, to, from) {
		return Link{}, ErrCycle
	}
	id, err := newID()
	if err != nil {
		return Link{}, err
	}
	added := Link{ID: id, From: from, To: to, Label: trimLabel(label), Auto: auto}
	s.Links = append(s.Links, added)
	return added, nil
}

// Find returns a pointer into the state, for a mutation in place.
func (s *State) Find(id string) (*Link, error) {
	for index := range s.Links {
		if s.Links[index].ID == id {
			return &s.Links[index], nil
		}
	}
	return nil, ErrNotFound
}

func (s *State) Update(id string, patch Patch) (Link, error) {
	link, err := s.Find(id)
	if err != nil {
		return Link{}, err
	}
	if patch.Label != nil {
		link.Label = trimLabel(*patch.Label)
	}
	if patch.Auto != nil {
		link.Auto = *patch.Auto
	}
	return *link, nil
}

func (s *State) Remove(id string) error {
	for index, link := range s.Links {
		if link.ID == id {
			s.Links = append(s.Links[:index], s.Links[index+1:]...)
			return nil
		}
	}
	return ErrNotFound
}

// RemoveAgent drops every link touching an agent, and whatever it was
// owed — for when the agent is deleted.
func (s *State) RemoveAgent(agentID string) {
	kept := s.Links[:0]
	for _, link := range s.Links {
		if link.From != agentID && link.To != agentID {
			kept = append(kept, link)
		}
	}
	s.Links = kept
	delete(s.Inbound, agentID)
}

// From is every link leaving an agent; To is every link arriving.
func (s *State) From(agentID string) []*Link {
	var found []*Link
	for index := range s.Links {
		if s.Links[index].From == agentID {
			found = append(found, &s.Links[index])
		}
	}
	return found
}

func (s *State) To(agentID string) []*Link {
	var found []*Link
	for index := range s.Links {
		if s.Links[index].To == agentID {
			found = append(found, &s.Links[index])
		}
	}
	return found
}

// TakeInbound returns the hop an agent's current turn started at — zero
// for a turn a human started — and forgets it, since the turn is over.
func (s *State) TakeInbound(agentID string) int {
	inbound, ok := s.Inbound[agentID]
	if !ok {
		return 0
	}
	delete(s.Inbound, agentID)
	return inbound.Hop
}

// Delivered records that a link fired into its target at a hop.
func (s *State) Delivered(link *Link, hop int, at time.Time) {
	link.LastFired = at
	link.Pending = nil
	if s.Inbound == nil {
		s.Inbound = map[string]Inbound{}
	}
	s.Inbound[link.To] = Inbound{Link: link.ID, Hop: hop + 1, At: at}
}

func trimLabel(label string) string {
	label = strings.TrimSpace(label)
	if runes := []rune(label); len(runes) > LabelLimit {
		return string(runes[:LabelLimit])
	}
	return label
}

func newID() (string, error) {
	var raw [8]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("generate link id: %w", err)
	}
	return hex.EncodeToString(raw[:]), nil
}

// Store is the state on disk: one JSON file beside the workspace catalog,
// read and written under a file lock, because the writers are separate
// processes — every provider hook is one, and so is every dashboard.
type Store struct {
	path string
}

const lockTimeout = 5 * time.Second

func NewStore() *Store {
	return NewStoreAt(statePath())
}

func NewStoreAt(path string) *Store {
	return &Store{path: path}
}

func (s *Store) Path() string { return s.path }

// Load reads the state; a missing file is an empty state.
func (s *Store) Load() (State, error) {
	var state State
	raw, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return state, nil
	}
	if err != nil {
		return state, fmt.Errorf("read links: %w", err)
	}
	if err := json.Unmarshal(raw, &state); err != nil {
		return State{}, fmt.Errorf("decode links: %w", err)
	}
	return state, nil
}

// Mutate runs a change against the state under the lock and writes what
// results. A change that returns an error writes nothing.
func (s *Store) Mutate(change func(*State) error) error {
	if s.path == "" {
		return errors.New("links: no state directory")
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return fmt.Errorf("create links directory: %w", err)
	}
	unlock, err := filelock.Acquire(s.path+".lock", 0o600, lockTimeout)
	if err != nil {
		return fmt.Errorf("lock links: %w", err)
	}
	defer unlock()
	state, err := s.Load()
	if err != nil {
		return err
	}
	if err := change(&state); err != nil {
		return err
	}
	encoded, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("encode links: %w", err)
	}
	temp := s.path + ".tmp"
	if err := os.WriteFile(temp, encoded, 0o600); err != nil {
		return fmt.Errorf("write links: %w", err)
	}
	if err := os.Rename(temp, s.path); err != nil {
		return fmt.Errorf("replace links: %w", err)
	}
	return nil
}

func statePath() string {
	if configured := os.Getenv("STORMLIGHT_LINKS_FILE"); configured != "" {
		return configured
	}
	if stateHome := os.Getenv("XDG_STATE_HOME"); stateHome != "" {
		return filepath.Join(stateHome, "stormlight", "links.json")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".local", "state", "stormlight", "links.json")
}
