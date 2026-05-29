package email

import (
	"strings"
	"testing"
)

func sampleData() RenderData {
	return RenderData{
		AppName:        "Talyvor Track",
		Heading:        "Something happened",
		IssueKey:       "ENG-42",
		Title:          "Fix the login bug",
		Lines:          []string{"Detail line one.", "Detail line two."},
		CTALabel:       "View issue",
		CTAURL:         "https://track.example.com/issues/ENG-42",
		PreferencesURL: "https://track.example.com/preferences",
	}
}

func TestRenderer_RendersEveryEventWithoutError(t *testing.T) {
	r, err := NewRenderer()
	if err != nil {
		t.Fatalf("NewRenderer: %v", err)
	}
	for _, ev := range trackEventTemplates {
		html, text, err := r.Render(ev, sampleData())
		if err != nil {
			t.Fatalf("Render(%q): %v", ev, err)
		}
		if strings.TrimSpace(html) == "" {
			t.Fatalf("Render(%q): empty HTML", ev)
		}
		if strings.TrimSpace(text) == "" {
			t.Fatalf("Render(%q): empty text", ev)
		}
		if !strings.Contains(html, "https://track.example.com/issues/ENG-42") {
			t.Errorf("Render(%q): HTML missing deep link", ev)
		}
		if !strings.Contains(html, "https://track.example.com/preferences") {
			t.Errorf("Render(%q): HTML missing preferences link", ev)
		}
		if !strings.Contains(text, "https://track.example.com/issues/ENG-42") {
			t.Errorf("Render(%q): text missing deep link", ev)
		}
	}
}

func TestRenderer_UnknownEventErrors(t *testing.T) {
	r, _ := NewRenderer()
	if _, _, err := r.Render("does.not.exist", sampleData()); err == nil {
		t.Fatal("rendering an unknown event should return an error, not silently succeed")
	}
}

func TestRenderer_EscapesUserSuppliedContent(t *testing.T) {
	r, _ := NewRenderer()
	d := sampleData()
	d.Title = `<script>alert(1)</script>`
	html, _, err := r.Render(EventIssueAssigned, d)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if strings.Contains(html, "<script>alert(1)</script>") {
		t.Error("user-supplied Title must be HTML-escaped in the email body")
	}
}
