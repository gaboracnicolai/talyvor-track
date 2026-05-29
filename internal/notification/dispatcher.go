package notification

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/talyvor/track/internal/email"
	"github.com/talyvor/track/internal/model"
)

var errNotFound = errors.New("notification: not found")

// IssueRef is the minimal issue data the dispatcher needs (loaded for comment
// notifications, where the handler only has the issue ID).
type IssueRef struct {
	ID          string
	WorkspaceID string
	Identifier  string
	Title       string
	CreatorID   string
	AssigneeID  *string
}

// directory resolves the people involved in an event and their addresses.
// Backed by the DB in production; faked in tests.
type directory interface {
	MembersByIDs(ctx context.Context, ids []string) (map[string]model.Member, error)
	IssueParticipants(ctx context.Context, issueID string) ([]string, error)
	SprintMembers(ctx context.Context, cycleID string) ([]string, error)
	ResolveMentions(ctx context.Context, workspaceID string, handles []string) ([]string, error)
	LoadIssue(ctx context.Context, issueID string) (*IssueRef, error)
}

// prefChecker filters recipients by their per-event email preference.
type prefChecker interface {
	EnabledMembers(ctx context.Context, eventType string, memberIDs []string) ([]string, error)
}

// enqueuer hands a rendered message to async delivery. *email.Queue satisfies
// it; it never blocks and may drop on overflow.
type enqueuer interface {
	Enqueue(email.Message) bool
}

// Dispatcher turns Track issue/sprint events into emails. Every method is
// best-effort: it resolves recipients, excludes the actor, honours preferences,
// renders, and enqueues — logging and swallowing all errors so a notification
// can never block or fail the core request that triggered it.
type Dispatcher struct {
	dir      directory
	prefs    prefChecker
	queue    enqueuer
	renderer *email.Renderer
	baseURL  string
	appName  string
	logger   *slog.Logger
}

func newDispatcher(dir directory, prefs prefChecker, queue enqueuer, renderer *email.Renderer, baseURL, appName string, logger *slog.Logger) *Dispatcher {
	if logger == nil {
		logger = slog.Default()
	}
	return &Dispatcher{
		dir: dir, prefs: prefs, queue: queue, renderer: renderer,
		baseURL: strings.TrimRight(baseURL, "/"), appName: appName, logger: logger,
	}
}

// IssueCreated handles assignment (when created with an assignee) and any
// mentions in the description.
func (d *Dispatcher) IssueCreated(ctx context.Context, issue model.Issue, actorID string) {
	if a := deref(issue.AssigneeID); a != "" {
		d.notifyAssigned(ctx, issueRefOf(issue), actorID)
	}
	d.notifyMentions(ctx, issueRefOf(issue), issue.Description, actorID)
}

// IssueUpdated handles assignee changes, status changes, and mentions in an
// updated description.
func (d *Dispatcher) IssueUpdated(ctx context.Context, issue model.Issue, updates map[string]any, actorID string) {
	if _, ok := updates["assignee_id"]; ok {
		if a := deref(issue.AssigneeID); a != "" {
			d.notifyAssigned(ctx, issueRefOf(issue), actorID)
		}
	}
	if _, ok := updates["status"]; ok {
		d.notifyStatusChanged(ctx, issueRefOf(issue), string(issue.Status), actorID)
	}
	if _, ok := updates["description"]; ok {
		d.notifyMentions(ctx, issueRefOf(issue), issue.Description, actorID)
	}
}

// IssueCommented notifies watchers (issue participants, minus the commenter)
// and anyone mentioned in the comment body.
func (d *Dispatcher) IssueCommented(ctx context.Context, issueID string, comment model.Comment, actorID string) {
	ref, err := d.dir.LoadIssue(ctx, issueID)
	if err != nil {
		d.logger.Warn("email: comment notify: load issue failed", slog.String("issue", issueID), slog.String("err", err.Error()))
		return
	}
	participants, err := d.dir.IssueParticipants(ctx, issueID)
	if err != nil {
		d.logger.Warn("email: comment notify: participants failed", slog.String("err", err.Error()))
	} else {
		excerpt := truncate(comment.Body, 200)
		d.fanout(ctx, email.EventIssueCommented, participants, actorID,
			fmt.Sprintf("New comment on %s", ref.Identifier),
			email.RenderData{
				Heading:  fmt.Sprintf("New comment on %s", ref.Identifier),
				IssueKey: ref.Identifier, Title: ref.Title,
				Lines:    []string{excerpt},
				CTALabel: "View discussion", CTAURL: d.issueURL(ref.Identifier),
			})
	}
	d.notifyMentions(ctx, *ref, comment.Body, actorID)
}

// SprintStarted / SprintEnded notify the sprint's members.
func (d *Dispatcher) SprintStarted(ctx context.Context, cycle model.Cycle, actorID string) {
	d.notifySprint(ctx, email.EventSprintStarted, cycle, actorID, "Sprint started")
}
func (d *Dispatcher) SprintEnded(ctx context.Context, cycle model.Cycle, actorID string) {
	d.notifySprint(ctx, email.EventSprintEnded, cycle, actorID, "Sprint ended")
}

// --- per-event helpers ---

func (d *Dispatcher) notifyAssigned(ctx context.Context, ref IssueRef, actorID string) {
	assignee := deref(ref.AssigneeID)
	d.fanout(ctx, email.EventIssueAssigned, []string{assignee}, actorID,
		fmt.Sprintf("%s assigned to you", ref.Identifier),
		email.RenderData{
			Heading:  fmt.Sprintf("%s assigned to you", ref.Identifier),
			IssueKey: ref.Identifier, Title: ref.Title,
			CTALabel: "View issue", CTAURL: d.issueURL(ref.Identifier),
		})
}

func (d *Dispatcher) notifyStatusChanged(ctx context.Context, ref IssueRef, status, actorID string) {
	recipients := append([]string{ref.CreatorID}, d.participantsOrEmpty(ctx, ref.ID)...)
	d.fanout(ctx, email.EventIssueStatusChanged, recipients, actorID,
		fmt.Sprintf("%s moved to %s", ref.Identifier, status),
		email.RenderData{
			Heading:  fmt.Sprintf("%s moved to %s", ref.Identifier, status),
			IssueKey: ref.Identifier, Title: ref.Title,
			Lines:    []string{fmt.Sprintf("Status is now: %s", status)},
			CTALabel: "View issue", CTAURL: d.issueURL(ref.Identifier),
		})
}

func (d *Dispatcher) notifyMentions(ctx context.Context, ref IssueRef, text, actorID string) {
	handles := parseMentions(text)
	if len(handles) == 0 {
		return
	}
	ids, err := d.dir.ResolveMentions(ctx, ref.WorkspaceID, handles)
	if err != nil {
		d.logger.Warn("email: resolve mentions failed", slog.String("err", err.Error()))
		return
	}
	if len(ids) == 0 {
		return
	}
	d.fanout(ctx, email.EventIssueMentioned, ids, actorID,
		fmt.Sprintf("You were mentioned on %s", ref.Identifier),
		email.RenderData{
			Heading:  fmt.Sprintf("You were mentioned on %s", ref.Identifier),
			IssueKey: ref.Identifier, Title: ref.Title,
			CTALabel: "View issue", CTAURL: d.issueURL(ref.Identifier),
		})
}

func (d *Dispatcher) notifySprint(ctx context.Context, event string, cycle model.Cycle, actorID, verb string) {
	members, err := d.dir.SprintMembers(ctx, cycle.ID)
	if err != nil {
		d.logger.Warn("email: sprint members failed", slog.String("err", err.Error()))
		return
	}
	d.fanout(ctx, event, members, actorID,
		fmt.Sprintf("%s: %s", verb, cycle.Name),
		email.RenderData{
			Heading:  fmt.Sprintf("%s: %s", verb, cycle.Name),
			Title:    cycle.Name,
			CTALabel: "View sprint", CTAURL: d.sprintURL(cycle.ID),
		})
}

// fanout is the shared pipeline: dedupe + exclude actor → preference filter →
// load addresses → render once → enqueue one message per recipient.
func (d *Dispatcher) fanout(ctx context.Context, event string, recipientIDs []string, actorID, subject string, data email.RenderData) {
	ids := dedupeExclude(recipientIDs, actorID)
	if len(ids) == 0 {
		return
	}
	enabled, err := d.prefs.EnabledMembers(ctx, event, ids)
	if err != nil {
		d.logger.Warn("email: preference filter failed", slog.String("event", event), slog.String("err", err.Error()))
		return
	}
	if len(enabled) == 0 {
		return
	}
	members, err := d.dir.MembersByIDs(ctx, enabled)
	if err != nil {
		d.logger.Warn("email: load member addresses failed", slog.String("err", err.Error()))
		return
	}

	data.AppName = d.appName
	data.PreferencesURL = d.baseURL + "/settings/notifications"
	html, text, err := d.renderer.Render(event, data)
	if err != nil {
		d.logger.Warn("email: render failed", slog.String("event", event), slog.String("err", err.Error()))
		return
	}

	for _, id := range enabled {
		m, ok := members[id]
		if !ok || m.Email == "" {
			continue
		}
		d.queue.Enqueue(email.Message{
			To: []string{m.Email}, Subject: subject, HTMLBody: html, TextBody: text,
		})
	}
}

func (d *Dispatcher) participantsOrEmpty(ctx context.Context, issueID string) []string {
	p, err := d.dir.IssueParticipants(ctx, issueID)
	if err != nil {
		d.logger.Warn("email: participants failed", slog.String("err", err.Error()))
		return nil
	}
	return p
}

func (d *Dispatcher) issueURL(identifier string) string { return d.baseURL + "/issues/" + identifier }
func (d *Dispatcher) sprintURL(cycleID string) string   { return d.baseURL + "/cycles/" + cycleID }

// --- helpers ---

func issueRefOf(i model.Issue) IssueRef {
	return IssueRef{
		ID: i.ID, WorkspaceID: i.WorkspaceID, Identifier: i.Identifier,
		Title: i.Title, CreatorID: i.CreatorID, AssigneeID: i.AssigneeID,
	}
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func dedupeExclude(ids []string, exclude string) []string {
	seen := map[string]bool{}
	var out []string
	for _, id := range ids {
		if id == "" || id == exclude || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	return out
}

func truncate(s string, max int) string {
	s = strings.TrimSpace(s)
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}
