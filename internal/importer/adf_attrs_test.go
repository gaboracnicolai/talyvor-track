package importer

import (
	"encoding/json"
	"strings"
	"testing"
)

// adf_attrs_test.go — the mapper half of "an ADF node's payload can live in `attrs`".
//
// ⚠ EVERY ASSERTION HERE GOES THROUGH mapJiraIssues, NOT THROUGH adfToText, AND THAT IS DELIBERATE.
// This merge changes adfToText's signature, so a test written against the helper would fail to
// COMPILE against today's code and a compile error is not a behavioural red — it proves the function
// moved, not that the product was wrong. mapJiraIssues' signature does not change, so every test in
// this file ran, and failed, against the shipped mapper before a line of it was edited.
//
// THE PROVENANCE (scripts/w34-jira-api-adf-probe.py, POST /rest/api/3/search/jql against a real Jira
// CLOUD site, anonymous, negative-controlled first). Over 2,000 issues of project HHH, 1,828 of
// which have a description at all:
//
//	descriptions carrying >=1 attrs-borne node    587 of 1,828 (32.1%)
//	  inlineCard (a URL)                          753
//	  media / mediaInline (an attachment)          90
//	  mention                                      34
//	  emoji                                        11
//	descriptions with NO `text` node at all          6 of 1,828
//
// ⚠ THE MAPPING IS JIRA'S OWN ANSWER, NOT A CHOICE MADE HERE. `expand: renderedFields` on the same
// endpoint shows what Atlassian renders each node as, and it is the attribute this table names:
//
//	inlineCard {"url":"https://…/browse/HHH-20738"}  →  <a href="…">https://…/browse/HHH-20738</a>
//	emoji      {"shortName":":smiley:","text":"😃"}   →  … such a request 😃
//	mention    {"text":"@Steve Ebersole", …}          →  Steve Ebersole — the "@" is DROPPED by
//	                                                    Atlassian's HTML renderer; see adf_attrs.go
//
// media/mediaInline render as <img> and an attachment link — there IS no text equivalent, so they
// are REPORTED rather than invented, which is this package's rule for every other field.

// adfIssueJSON wraps a raw ADF document as the one field this file is about. Everything else is the
// minimum mapJiraIssues needs; no dates, so no other note kind can be confused with these.
func adfIssueJSON(key, adf string) string {
	return `{"key":"` + key + `","fields":{"summary":"s","description":` + adf +
		`,"status":{"name":"Done"},"priority":{"name":"Medium"},"labels":[]}}`
}

func mapOneADF(t *testing.T, adf string) mappedIssue {
	t.Helper()
	var page jiraResp
	if err := json.Unmarshal([]byte(jiraAPIPage(adfIssueJSON("PROJ-1", adf))), &page); err != nil {
		t.Fatalf("fixture does not decode: %v", err)
	}
	got := mapJiraIssues(page.Issues)
	if len(got) != 1 {
		t.Fatalf("mapJiraIssues returned %d issues, want 1", len(got))
	}
	return got[0]
}

// descriptionNotes returns only the notes this merge is about, so an unrelated note kind added later
// cannot make these tests pass or fail by accident.
func descriptionNotes(m mappedIssue) []FieldNote {
	var out []FieldNote
	for _, n := range m.notes {
		if n.Field == fieldDescription {
			out = append(out, n)
		}
	}
	return out
}

// ⚠ THE SENTENCE IS COPIED FROM A REAL ISSUE, HHH-20742, NOT INVENTED. Jira renders it "Follow up to
// https://hibernate.atlassian.net/browse/HHH-20738  - remove the deprecated stuff"; today Track
// imports "Follow up to   - remove the deprecated stuff" and the issue it follows up on is gone.
func TestJiraAPI_AnInlineCardsURLReachesTheDescription(t *testing.T) {
	const url = "https://hibernate.atlassian.net/browse/HHH-20738"
	m := mapOneADF(t, `{"type":"doc","version":1,"content":[{"type":"paragraph","content":[
		{"type":"text","text":"Follow up to "},
		{"type":"inlineCard","attrs":{"url":"`+url+`","localId":"fa0952164cc3"}},
		{"type":"text","text":" - remove the deprecated stuff"}]}]}`)

	// ⚠ AN EXACT STRING, NOT strings.Contains, AND A CONTROL IS WHY. `Contains(desc, url)` stays
	// GREEN if the attribute is emitted as its raw JSON — `"https://…"`, quotes and all — because
	// the quoted form contains the unquoted one. The sentence is what Jira renders, so the sentence
	// is what is asserted.
	const want = "Follow up to " + url + " - remove the deprecated stuff"
	if m.issue.Description != want {
		t.Errorf("description = %q, want %q.\n"+
			"A bare URL in a Jira description is stored as an `inlineCard` whose whole payload is "+
			"attrs.url — 753 of them across 1,828 real descriptions — and the flattener reads only `text` "+
			"nodes, so the link vanishes and the sentence reads as broken prose.",
			m.issue.Description, want)
	}
	// The localId is UI identity, not content: an operator must not find it in their description.
	if strings.Contains(m.issue.Description, "fa0952164cc3") {
		t.Errorf("description = %q — it carries the node's localId, which is not content", m.issue.Description)
	}
	if n := descriptionNotes(m); len(n) != 0 {
		t.Errorf("a link that DID import produced %d description note(s): %#v — nothing was lost here", len(n), n)
	}
}

// A description whose only content is a link imports as the EMPTY STRING today. Measured: 6 of the
// 1,828 real descriptions carry no `text` node at all.
func TestJiraAPI_ADescriptionThatIsOnlyALinkIsNotEmpty(t *testing.T) {
	m := mapOneADF(t, `{"type":"doc","version":1,"content":[{"type":"paragraph","content":[
		{"type":"inlineCard","attrs":{"url":"https://github.com/hibernate/hibernate-orm/pull/732"}}]}]}`)
	if strings.TrimSpace(m.issue.Description) == "" {
		t.Errorf("description = %q — the issue's entire description was a link and it imported as "+
			"nothing, with the job reporting {imported:1, warnings:[]}", m.issue.Description)
	}
}

// mention and emoji both keep their visible text in attrs.text — Jira's own renderer emits exactly
// that string. The two are asserted together because they are one rule, and separately from
// inlineCard because they name a DIFFERENT attribute.
func TestJiraAPI_AMentionAndAnEmojiKeepTheirText(t *testing.T) {
	m := mapOneADF(t, `{"type":"doc","version":1,"content":[{"type":"paragraph","content":[
		{"type":"text","text":"ping "},
		{"type":"mention","attrs":{"id":"557058:aafa","text":"@Steve Ebersole","accessLevel":""}},
		{"type":"text","text":" thanks "},
		{"type":"emoji","attrs":{"shortName":":pray:","id":"1f64f","text":"🙏"}}]}]}`)

	for _, want := range []string{"@Steve Ebersole", "\U0001F64F"} {
		if !strings.Contains(m.issue.Description, want) {
			t.Errorf("description = %q, missing %q — Jira stores this node's visible text in "+
				"attrs.text and renders exactly that string", m.issue.Description, want)
		}
	}
	// The account id is not content either.
	if strings.Contains(m.issue.Description, "557058") {
		t.Errorf("description = %q — it carries the mention's account id", m.issue.Description)
	}
}

// ⚠ THE OTHER HALF OF THE RULE, AND THE HALF THAT KEEPS IT HONEST. An attachment has NO text
// equivalent in Jira's own rendering, so there is nothing to place — and a drop nobody is told about
// is indistinguishable from the silent ones #71/#72/#73/#83/#87 each found one field over.
func TestJiraAPI_AnAttachmentInTheDescriptionIsReported(t *testing.T) {
	m := mapOneADF(t, `{"type":"doc","version":1,"content":[
		{"type":"paragraph","content":[{"type":"text","text":"see the screenshot"}]},
		{"type":"mediaSingle","content":[{"type":"media","attrs":{"type":"file",
			"id":"51029fdf","alt":"Screenshot 2026-05-27.png","collection":"","height":725,"width":568}}]}]}`)

	notes := descriptionNotes(m)
	if len(notes) != 1 {
		t.Fatalf("description notes = %#v, want exactly one — an attachment referenced in a "+
			"description does not come across and nothing says so", notes)
	}
	if notes[0].Value != "media" || notes[0].Via != viaADFNodeDropped {
		t.Errorf("note = %#v, want Value=\"media\" Via=%q — the warning has to NAME the node type, "+
			"or an operator cannot tell which part of their description is missing", notes[0], viaADFNodeDropped)
	}
	if !strings.Contains(m.issue.Description, "see the screenshot") {
		t.Errorf("description = %q — the surrounding prose must still import", m.issue.Description)
	}
}

// Notes are COUNTED (map[FieldNote]int), so one issue with three attachments must contribute ONE
// note or the warning will say "3 issue(s)" about a single issue.
func TestJiraAPI_ThreeAttachmentsOnOneIssueAreOneNote(t *testing.T) {
	m := mapOneADF(t, `{"type":"doc","version":1,"content":[
		{"type":"mediaSingle","content":[{"type":"media","attrs":{"id":"a","alt":"a.png"}}]},
		{"type":"mediaSingle","content":[{"type":"media","attrs":{"id":"b","alt":"b.png"}}]},
		{"type":"paragraph","content":[{"type":"text","text":"x"},
			{"type":"media","attrs":{"id":"c","alt":"c.png"}}]}]}`)

	if notes := descriptionNotes(m); len(notes) != 1 {
		t.Errorf("description notes = %#v, want exactly one for three attachments on ONE issue — "+
			"the pipeline counts notes, so duplicates are reported as extra issues", notes)
	}
}

// Two DIFFERENT dropped node types on one issue are two notes: an operator who is told "media" must
// not have "mediaInline" folded into it, because they point at different things in the editor.
func TestJiraAPI_TwoDifferentDroppedTypesAreTwoNotes(t *testing.T) {
	m := mapOneADF(t, `{"type":"doc","version":1,"content":[{"type":"paragraph","content":[
		{"type":"text","text":"x"},
		{"type":"media","attrs":{"id":"a","alt":"a.png"}},
		{"type":"mediaInline","attrs":{"id":"b","collection":"","type":"file"}}]}]}`)

	notes := descriptionNotes(m)
	if len(notes) != 2 {
		t.Fatalf("description notes = %#v, want two — media and mediaInline are different nodes", notes)
	}
	got := []string{notes[0].Value, notes[1].Value}
	if got[0] != "media" || got[1] != "mediaInline" {
		t.Errorf("note values = %v, want [media mediaInline] in that order — the order has to be "+
			"stable or the same import produces different warnings on different runs", got)
	}
}

// ⚠ THIS ONE PASSES AGAINST TODAY'S CODE AND IT IS STILL THE MOST IMPORTANT TEST IN THE FILE.
// The obvious implementation of "report a node whose payload we cannot read" is "report any leaf
// that carries attrs" — and it is WRONG, measured: over 2,000 real issues an empty `paragraph`
// carries attrs.localId 88 times, `rule` 17 times and an empty `heading` 4 times, all of them UI
// identity rather than content. That rule would have manufactured 109 warnings over those 2,000
// issues about nothing at all. This is the assertion that says so, and the control harness holds the
// mutation that proves it can fail.
func TestJiraAPI_EmptyStructuralNodesAreNotReportedAsLost(t *testing.T) {
	m := mapOneADF(t, `{"type":"doc","version":1,"content":[
		{"type":"paragraph","attrs":{"localId":"p1"}},
		{"type":"rule","attrs":{"localId":"r1"}},
		{"type":"heading","attrs":{"level":3,"localId":"h1"}},
		{"type":"paragraph","content":[{"type":"text","text":"a"},{"type":"hardBreak"},
			{"type":"text","text":"b"}]}]}`)

	if notes := descriptionNotes(m); len(notes) != 0 {
		t.Errorf("description notes = %#v, want none.\nAn empty paragraph, a horizontal rule and an "+
			"empty heading all carry attrs.localId on the real instance and none of them is lost "+
			"content — a rule keyed on \"leaf with attrs\" reports 105 of these per 2,000 issues.", notes)
	}
	if m.issue.Description != "a\nb" {
		t.Errorf("description = %q, want \"a\\nb\" — the hardBreak and the empty blocks must not "+
			"change what the text says", m.issue.Description)
	}
}

// An inlineCard whose attrs carry no url is a node this importer placed nothing for — the same
// report as an attachment, and for the same reason. Without this the `else` arm in walkADF is a
// branch no test is answerable for.
func TestJiraAPI_ANodeWhosePinnedAttributeIsMissingIsReported(t *testing.T) {
	m := mapOneADF(t, `{"type":"doc","version":1,"content":[{"type":"paragraph","content":[
		{"type":"text","text":"see "},
		{"type":"inlineCard","attrs":{"localId":"e6af"}}]}]}`)

	notes := descriptionNotes(m)
	if len(notes) != 1 || notes[0].Value != "inlineCard" {
		t.Fatalf("description notes = %#v, want one naming inlineCard — the node carried something "+
			"and this importer placed none of it, which is the whole reason the report exists", notes)
	}
}

// The older API sent description as a plain string. That path predates this merge and must survive
// it unchanged — it is the must-stay-green companion for every control that edits the flattener.
func TestJiraAPI_APlainStringDescriptionStillImports(t *testing.T) {
	m := mapOneADF(t, `"plain description"`)
	if m.issue.Description != "plain description" {
		t.Errorf("description = %q, want the string through unchanged", m.issue.Description)
	}
	if notes := descriptionNotes(m); len(notes) != 0 {
		t.Errorf("a plain-string description produced notes: %#v", notes)
	}
}

// The rendered warning is what an operator actually reads. It must name the node type and say what
// was lost; asserting the FieldNote alone would pass on a note nothing renders.
func TestJiraAPI_TheDroppedNodeWarningNamesTheNodeAndTheLoss(t *testing.T) {
	line := FieldNote{Field: fieldDescription, Value: "media", Via: viaADFNodeDropped}.render(7)
	for _, want := range []string{`"media"`, "7 issue(s)", "description"} {
		if !strings.Contains(line, want) {
			t.Errorf("rendered warning %q does not contain %q", line, want)
		}
	}
	// It must not be the generic fallback sentence, which says "imported as" and names nothing.
	if strings.Contains(line, "unrecognised description") {
		t.Errorf("rendered warning %q fell through to the default branch", line)
	}
}

// ─── blockCard — the type the census bound hid ────────────────────────────────────────────────────
//
// ⚠ THE NODE BELOW IS COPIED FROM A REAL ISSUE, HHH-18501, NOT INVENTED. It is `inlineCard`'s
// BLOCK-LEVEL twin: same payload attribute, no `text` child, and NOTHING in adf_attrs.go's two
// tables — so today its URL reaches neither the description nor the search index, and no note says
// so. That is the exact defect adf_attrs.go was written to close, one node type over.
//
// ⚠ THE MAPPING IS JIRA'S OWN ANSWER, MEASURED THE SAME WAY inlineCard's WAS, NOT REASONED FROM THE
// NAME. `expand: renderedFields` on the same endpoint, on THIS issue, returns Atlassian's own HTML
// for the same document and it contains attrs.url verbatim:
//
//	blockCard {"url":"https://github.com/hibernate/hibernate-test-case-templates/pull/421"}
//	   → Jira's rendered description contains that exact string
//
// ⚠ WHY THE SHIPPED PROBE DID NOT FIND IT, WHICH IS THE HALF WORTH KEEPING. adf_attrs.go names
// scripts/w34-jira-api-adf-probe.py as "the only thing that would notice" a new attrs-borne type,
// and that probe FAILS on one. It printed "unpinned attrs-borne leaf types: NONE" — a sentence about
// the PROJECT — after reading PAGES=20 × 100 = 2,000 of the project's ~20,550 issues. blockCard
// occurs ONCE in the first 3,000. The guard was not wrong; its negative was narrower than the
// sentence it printed, and the script now says which population its NONE is about.
func TestJiraAPI_ABlockCardsURLReachesTheDescription(t *testing.T) {
	const url = "https://github.com/hibernate/hibernate-test-case-templates/pull/421"
	m := mapOneADF(t, `{"type":"doc","version":1,"content":[
		{"type":"paragraph","content":[{"type":"text","text":"reproducer:"}]},
		{"type":"blockCard","attrs":{"url":"`+url+`"}}]}`)

	// ⚠ AN EXACT STRING, AND A CONTROL IS WHY — THE SAME CONTROL, FOR THE SAME REASON, THAT THE
	// inlineCard test above already states. The first draft of this test asserted
	// `strings.Contains(desc, url)` plus a guard on the substring `"url"`, and control C3
	// (scripts-side harness: make adfAttrString return the attribute's RAW JSON) went GREEN on it —
	// because the quoted form `"https://…"` CONTAINS the unquoted one, and the raw bytes of the
	// VALUE never contain the KEY. A `Contains` here would have passed on a description carrying
	// quoted JSON. The exact sentence is what Jira renders, so the exact sentence is what is pinned.
	const want = "reproducer:\n" + url
	if m.issue.Description != want {
		t.Errorf("description = %q, want %q.\n"+
			"A blockCard's whole payload is attrs.url and it has no `text` child, so the flattener "+
			"emits nothing for it. `description` is also the product's only full-text index "+
			"(issue.Store.Search), so the issue is unfindable by the link that is its reproducer, "+
			"and the import reports {imported:1, warnings:[]}.", m.issue.Description, want)
	}
	if n := descriptionNotes(m); len(n) != 0 {
		t.Errorf("a blockCard whose URL DID import produced %d description note(s): %#v — "+
			"nothing was lost here", len(n), n)
	}
}

// The same node ALONE — the shape that makes the loss total rather than partial. Today this
// description imports as the empty string and the job says {imported:1, warnings:[]}: an operator
// cannot tell it from an issue that never had a description.
func TestJiraAPI_ADescriptionThatIsOnlyABlockCardIsNotEmpty(t *testing.T) {
	m := mapOneADF(t, `{"type":"doc","version":1,"content":[
		{"type":"blockCard","attrs":{"url":"https://github.com/hibernate/hibernate-orm/pull/732"}}]}`)
	if strings.TrimSpace(m.issue.Description) == "" {
		t.Errorf("description = %q — the issue's entire description was a linked card and it "+
			"imported as nothing, silently", m.issue.Description)
	}
}
