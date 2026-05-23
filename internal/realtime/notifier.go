package realtime

import (
	"context"
	"time"

	"github.com/talyvor/track/internal/model"
)

// Notifier is the typed surface store-side code uses to push events
// through the hub. Without it every store would have to construct
// Event values by hand and remember to broadcast to both the team
// and workspace rooms.
type Notifier struct {
	hub *Hub
}

func NewNotifier(hub *Hub) *Notifier { return &Notifier{hub: hub} }

// IssueCreated fans out to both the team room (so team boards
// refresh) and the workspace room (so global activity feeds catch it).
// The team room broadcast carries the actor ID so the creator's
// browser tab — already showing the new issue — doesn't bounce a
// duplicate render.
func (n *Notifier) IssueCreated(_ context.Context, wsID, teamID, actorID string, issue model.Issue) {
	if n == nil || n.hub == nil {
		return
	}
	ev := Event{
		Type:        EventIssueCreated,
		WorkspaceID: wsID,
		ActorID:     actorID,
		Payload:     issue,
		Timestamp:   time.Now().UTC(),
	}
	n.hub.BroadcastToRoom("team:"+teamID, ev)
	n.hub.BroadcastToRoom("workspace:"+wsID, ev)
}

// IssueUpdated emits to the issue-specific room (for the detail
// drawer) AND the team room (for the board). The changes map is the
// patch the caller applied — small, structured, easy for the
// frontend to diff against local state.
func (n *Notifier) IssueUpdated(_ context.Context, wsID, teamID, issueID, actorID string, changes map[string]any) {
	if n == nil || n.hub == nil {
		return
	}
	ev := Event{
		Type:        EventIssueUpdated,
		WorkspaceID: wsID,
		ActorID:     actorID,
		Payload: map[string]any{
			"issue_id": issueID,
			"changes":  changes,
		},
		Timestamp: time.Now().UTC(),
	}
	n.hub.BroadcastToRoom("issue:"+issueID, ev)
	n.hub.BroadcastToRoom("team:"+teamID, ev)
}

func (n *Notifier) IssueDeleted(_ context.Context, wsID, teamID, issueID, actorID string) {
	if n == nil || n.hub == nil {
		return
	}
	ev := Event{
		Type:        EventIssueDeleted,
		WorkspaceID: wsID,
		ActorID:     actorID,
		Payload:     map[string]string{"issue_id": issueID},
		Timestamp:   time.Now().UTC(),
	}
	n.hub.BroadcastToRoom("issue:"+issueID, ev)
	n.hub.BroadcastToRoom("team:"+teamID, ev)
}

func (n *Notifier) CommentCreated(_ context.Context, wsID, issueID, actorID string, comment model.Comment) {
	if n == nil || n.hub == nil {
		return
	}
	ev := Event{
		Type:        EventCommentCreated,
		WorkspaceID: wsID,
		ActorID:     actorID,
		Payload:     comment,
		Timestamp:   time.Now().UTC(),
	}
	n.hub.BroadcastToRoom("issue:"+issueID, ev)
}

func (n *Notifier) CommentUpdated(_ context.Context, wsID, issueID, actorID string, comment model.Comment) {
	if n == nil || n.hub == nil {
		return
	}
	n.hub.BroadcastToRoom("issue:"+issueID, Event{
		Type:        EventCommentUpdated,
		WorkspaceID: wsID,
		ActorID:     actorID,
		Payload:     comment,
		Timestamp:   time.Now().UTC(),
	})
}

func (n *Notifier) CommentDeleted(_ context.Context, wsID, issueID, commentID, actorID string) {
	if n == nil || n.hub == nil {
		return
	}
	n.hub.BroadcastToRoom("issue:"+issueID, Event{
		Type:        EventCommentDeleted,
		WorkspaceID: wsID,
		ActorID:     actorID,
		Payload:     map[string]string{"comment_id": commentID},
		Timestamp:   time.Now().UTC(),
	})
}
