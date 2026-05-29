package email

import (
	"bytes"
	"embed"
	"fmt"
	htmltemplate "html/template"
	texttemplate "text/template"
)

// Event template names. These double as the notification_preferences
// event_type keys. Adding a product (e.g. Docs) means adding its own template
// files + names; the renderer parses whatever is embedded.
const (
	EventIssueAssigned      = "issue.assigned"
	EventIssueMentioned     = "issue.mentioned"
	EventIssueCommented     = "issue.commented"
	EventIssueStatusChanged = "issue.status_changed"
	EventSprintStarted      = "sprint.started"
	EventSprintEnded        = "sprint.ended"
)

// trackEventTemplates is the set of Track events that must have templates.
var trackEventTemplates = []string{
	EventIssueAssigned, EventIssueMentioned, EventIssueCommented,
	EventIssueStatusChanged, EventSprintStarted, EventSprintEnded,
}

//go:embed templates
var templatesFS embed.FS

// RenderData is the data every email template renders against. The dispatcher
// fills the per-event copy (Heading, Lines, CTA, …); Content is set internally
// when wrapping content in the HTML layout.
type RenderData struct {
	AppName        string
	Heading        string
	IssueKey       string
	Title          string
	Lines          []string
	CTALabel       string
	CTAURL         string
	PreferencesURL string

	Content htmltemplate.HTML // internal: rendered content, injected into layout
}

// Renderer renders event emails. HTML content is wrapped in a shared base
// layout; text bodies are self-contained. Safe for concurrent use after
// construction (templates are read-only).
type Renderer struct {
	html *htmltemplate.Template
	text *texttemplate.Template
}

func NewRenderer() (*Renderer, error) {
	h, err := htmltemplate.ParseFS(templatesFS, "templates/*.html.tmpl")
	if err != nil {
		return nil, fmt.Errorf("email: parse html templates: %w", err)
	}
	t, err := texttemplate.ParseFS(templatesFS, "templates/*.text.tmpl")
	if err != nil {
		return nil, fmt.Errorf("email: parse text templates: %w", err)
	}
	return &Renderer{html: h, text: t}, nil
}

// Render produces the HTML and text bodies for an event. Returns an error if
// the event has no template (so a typo fails loudly rather than sending blank).
func (r *Renderer) Render(event string, d RenderData) (htmlBody, textBody string, err error) {
	var content bytes.Buffer
	if err = r.html.ExecuteTemplate(&content, event, d); err != nil {
		return "", "", fmt.Errorf("email: render html content %q: %w", event, err)
	}
	d.Content = htmltemplate.HTML(content.String())

	var html bytes.Buffer
	if err = r.html.ExecuteTemplate(&html, "layout", d); err != nil {
		return "", "", fmt.Errorf("email: render layout: %w", err)
	}

	var text bytes.Buffer
	if err = r.text.ExecuteTemplate(&text, event, d); err != nil {
		return "", "", fmt.Errorf("email: render text %q: %w", event, err)
	}
	return html.String(), text.String(), nil
}
