package realtime

import (
	"sync"
	"time"
)

// presenceTTL is how long a viewer counts as "active" after their
// last heartbeat. Five minutes matches the spec — long enough to
// survive a brief tab-switch, short enough that ghost presence
// fades fast.
const presenceTTL = 5 * time.Minute

// Viewer represents a member who's currently looking at an issue.
type Viewer struct {
	MemberID  string    `json:"member_id"`
	Name      string    `json:"name"`
	AvatarURL string    `json:"avatar_url"`
	Since     time.Time `json:"since"`
}

// PresenceStore tracks who is currently viewing which issue.
// Memory-only — restarts wipe all state. That's deliberate: presence
// is ephemeral by definition.
type PresenceStore struct {
	mu      sync.RWMutex
	viewers map[string]map[string]Viewer // issueID → memberID → viewer
}

func NewPresenceStore() *PresenceStore {
	return &PresenceStore{viewers: make(map[string]map[string]Viewer)}
}

// AddViewer registers (or refreshes) a viewer's presence on an issue.
// Calling repeatedly with the same memberID resets the Since stamp —
// the UI can heartbeat every minute or so to keep presence alive.
func (p *PresenceStore) AddViewer(issueID string, v Viewer) {
	if v.Since.IsZero() {
		v.Since = time.Now().UTC()
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	room, ok := p.viewers[issueID]
	if !ok {
		room = make(map[string]Viewer)
		p.viewers[issueID] = room
	}
	room[v.MemberID] = v
}

func (p *PresenceStore) RemoveViewer(issueID, memberID string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if room, ok := p.viewers[issueID]; ok {
		delete(room, memberID)
		if len(room) == 0 {
			delete(p.viewers, issueID)
		}
	}
}

// GetViewers returns the viewers active in the last presenceTTL.
// Expired viewers are pruned lazily on read so we don't need a
// background sweeper goroutine.
func (p *PresenceStore) GetViewers(issueID string) []Viewer {
	cutoff := time.Now().UTC().Add(-presenceTTL)

	p.mu.Lock()
	defer p.mu.Unlock()
	room, ok := p.viewers[issueID]
	if !ok {
		return nil
	}
	var out []Viewer
	for memberID, v := range room {
		if v.Since.Before(cutoff) {
			delete(room, memberID)
			continue
		}
		out = append(out, v)
	}
	if len(room) == 0 {
		delete(p.viewers, issueID)
	}
	return out
}
