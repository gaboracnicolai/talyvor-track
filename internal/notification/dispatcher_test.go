package notification

import (
	"context"
	"testing"

	"github.com/talyvor/track/internal/email"
	"github.com/talyvor/track/internal/model"
)

// --- fakes ---

type fakeDir struct {
	members      map[string]model.Member
	participants map[string][]string // issueID -> member ids
	sprint       map[string][]string // cycleID -> member ids
	mentions     map[string][]string // token -> member ids
	issues       map[string]IssueRef
}

func (f *fakeDir) MembersByIDs(_ context.Context, ids []string) (map[string]model.Member, error) {
	out := map[string]model.Member{}
	for _, id := range ids {
		if m, ok := f.members[id]; ok {
			out[id] = m
		}
	}
	return out, nil
}
func (f *fakeDir) IssueParticipants(_ context.Context, issueID string) ([]string, error) {
	return f.participants[issueID], nil
}
func (f *fakeDir) SprintMembers(_ context.Context, cycleID string) ([]string, error) {
	return f.sprint[cycleID], nil
}
func (f *fakeDir) ResolveMentions(_ context.Context, _ string, handles []string) ([]string, error) {
	var out []string
	for _, h := range handles {
		out = append(out, f.mentions[h]...)
	}
	return out, nil
}
func (f *fakeDir) LoadIssue(_ context.Context, id string) (*IssueRef, error) {
	if r, ok := f.issues[id]; ok {
		return &r, nil
	}
	return nil, errNotFound
}

type spyQueue struct{ msgs []email.Message }

func (s *spyQueue) Enqueue(m email.Message) bool { s.msgs = append(s.msgs, m); return true }
func (s *spyQueue) recipients() map[string]bool {
	out := map[string]bool{}
	for _, m := range s.msgs {
		for _, to := range m.To {
			out[to] = true
		}
	}
	return out
}

type fakePrefs struct{ optedOut map[string]bool }

func (f fakePrefs) EnabledMembers(_ context.Context, _ string, ids []string) ([]string, error) {
	var out []string
	for _, id := range ids {
		if !f.optedOut[id] {
			out = append(out, id)
		}
	}
	return out, nil
}

func newTestDispatcher(t *testing.T, dir directory, prefs prefChecker) (*Dispatcher, *spyQueue) {
	t.Helper()
	r, err := email.NewRenderer()
	if err != nil {
		t.Fatalf("renderer: %v", err)
	}
	q := &spyQueue{}
	d := newDispatcher(dir, prefs, q, r, "https://track.example.com", "Talyvor Track", nil)
	return d, q
}

func mem(id, email string) model.Member { return model.Member{ID: id, Email: email, Name: id} }

// --- tests ---

func TestDispatcher_AssignmentEmailsAssigneeNotActor(t *testing.T) {
	dir := &fakeDir{members: map[string]model.Member{
		"creator":  mem("creator", "creator@x.z"),
		"assignee": mem("assignee", "assignee@x.z"),
	}}
	d, q := newTestDispatcher(t, dir, fakePrefs{})

	assignee := "assignee"
	issue := model.Issue{ID: "i1", Identifier: "ENG-1", Title: "Fix", CreatorID: "creator", AssigneeID: &assignee, WorkspaceID: "ws"}
	d.IssueUpdated(context.Background(), issue, map[string]any{"assignee_id": "assignee"}, "creator")

	if len(q.msgs) != 1 {
		t.Fatalf("want 1 email, got %d", len(q.msgs))
	}
	if !q.recipients()["assignee@x.z"] {
		t.Fatal("assignee should be emailed")
	}
	if q.recipients()["creator@x.z"] {
		t.Fatal("actor (creator) must never be emailed about their own action")
	}
}

func TestDispatcher_SelfAssignmentEmailsNobody(t *testing.T) {
	dir := &fakeDir{members: map[string]model.Member{"u": mem("u", "u@x.z")}}
	d, q := newTestDispatcher(t, dir, fakePrefs{})
	self := "u"
	issue := model.Issue{ID: "i1", Identifier: "ENG-1", CreatorID: "u", AssigneeID: &self, WorkspaceID: "ws"}
	d.IssueUpdated(context.Background(), issue, map[string]any{"assignee_id": "u"}, "u")
	if len(q.msgs) != 0 {
		t.Fatalf("self-assignment must email nobody, got %d", len(q.msgs))
	}
}

func TestDispatcher_PreferenceOptOutSuppresses(t *testing.T) {
	dir := &fakeDir{members: map[string]model.Member{
		"creator":  mem("creator", "creator@x.z"),
		"assignee": mem("assignee", "assignee@x.z"),
	}}
	d, q := newTestDispatcher(t, dir, fakePrefs{optedOut: map[string]bool{"assignee": true}})

	assignee := "assignee"
	issue := model.Issue{ID: "i1", Identifier: "ENG-1", CreatorID: "creator", AssigneeID: &assignee, WorkspaceID: "ws"}
	d.IssueUpdated(context.Background(), issue, map[string]any{"assignee_id": "assignee"}, "creator")

	if len(q.msgs) != 0 {
		t.Fatalf("opted-out assignee must receive nothing, got %d", len(q.msgs))
	}
}

func TestDispatcher_CommentEmailsWatchersExcludingCommenter(t *testing.T) {
	dir := &fakeDir{
		members: map[string]model.Member{
			"creator":   mem("creator", "creator@x.z"),
			"assignee":  mem("assignee", "assignee@x.z"),
			"commenter": mem("commenter", "commenter@x.z"),
		},
		participants: map[string][]string{"i1": {"creator", "assignee", "commenter"}},
		issues:       map[string]IssueRef{"i1": {ID: "i1", Identifier: "ENG-1", Title: "Fix", WorkspaceID: "ws"}},
	}
	d, q := newTestDispatcher(t, dir, fakePrefs{})

	comment := model.Comment{ID: "c1", IssueID: "i1", AuthorID: "commenter", Body: "looks good"}
	d.IssueCommented(context.Background(), "i1", comment, "commenter")

	rcpts := q.recipients()
	if !rcpts["creator@x.z"] || !rcpts["assignee@x.z"] {
		t.Fatalf("watchers creator+assignee should be emailed, got %v", rcpts)
	}
	if rcpts["commenter@x.z"] {
		t.Fatal("the commenter must be excluded from comment notifications")
	}
}

func TestDispatcher_MentionEmailsResolvedUser(t *testing.T) {
	dir := &fakeDir{
		members:      map[string]model.Member{"bob": mem("bob", "bob@x.z"), "author": mem("author", "author@x.z")},
		participants: map[string][]string{"i1": {"author"}}, // only the author watches → no watcher email
		issues:       map[string]IssueRef{"i1": {ID: "i1", Identifier: "ENG-1", WorkspaceID: "ws"}},
		mentions:     map[string][]string{"bob": {"bob"}},
	}
	d, q := newTestDispatcher(t, dir, fakePrefs{})

	comment := model.Comment{ID: "c1", IssueID: "i1", AuthorID: "author", Body: "hey @bob please review"}
	d.IssueCommented(context.Background(), "i1", comment, "author")

	if !q.recipients()["bob@x.z"] {
		t.Fatalf("mentioned user bob should be emailed, got %v", q.recipients())
	}
}

// TestDispatcher_SkipsRecipientsWithoutResolvedAddress is an anti-enumeration
// guard: recipients are addressed only by the address the directory resolves
// for their member ID. A member that the directory does not return (no row),
// or one with an empty email, is dropped — the dispatcher never invents or
// forwards an address, so notifications can only ever reach real account
// addresses, never an attacker-supplied one.
func TestDispatcher_SkipsRecipientsWithoutResolvedAddress(t *testing.T) {
	dir := &fakeDir{members: map[string]model.Member{
		// "ghost" deliberately absent from the directory (no member row).
		"noaddr":   mem("noaddr", ""), // present but no email on file
		"realuser": mem("realuser", "real@x.z"),
	}}
	d, q := newTestDispatcher(t, dir, fakePrefs{})

	assignee := "realuser"
	issue := model.Issue{
		ID: "i1", Identifier: "ENG-9", Title: "x",
		CreatorID: "ghost", AssigneeID: &assignee, WorkspaceID: "ws",
	}
	// status change notifies creator ("ghost", unresolved) + participants.
	d.notifyStatusChanged(context.Background(), issueRefOf(issue), "done", "someoneelse")

	for _, m := range q.msgs {
		for _, to := range m.To {
			if to == "" {
				t.Fatalf("dispatched a message with an empty recipient address: %+v", m)
			}
		}
	}
	// "ghost" (no row) and "noaddr" (empty email) must never be addressed.
	if q.recipients()[""] {
		t.Fatal("empty address must never be enqueued")
	}
}

func TestDispatcher_SprintStartedEmailsMembersNotActor(t *testing.T) {
	dir := &fakeDir{
		members: map[string]model.Member{"a": mem("a", "a@x.z"), "b": mem("b", "b@x.z")},
		sprint:  map[string][]string{"cy1": {"a", "b"}},
	}
	d, q := newTestDispatcher(t, dir, fakePrefs{})
	cycle := model.Cycle{ID: "cy1", Name: "Sprint 7", WorkspaceID: "ws", TeamID: "t1"}
	d.SprintStarted(context.Background(), cycle, "a")

	rcpts := q.recipients()
	if !rcpts["b@x.z"] {
		t.Fatal("sprint member b should be emailed")
	}
	if rcpts["a@x.z"] {
		t.Fatal("actor a must not be emailed about starting the sprint")
	}
}
