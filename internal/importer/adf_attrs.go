package importer

import "encoding/json"

// adf_attrs.go — the ADF nodes whose payload lives in `attrs` rather than in a child `text` node.
//
// ⚠ THE DEFECT THIS FILE EXISTS FOR. walkADF read `text` and nothing else, so every node that keeps
// its content in an attribute contributed NOTHING and said nothing about it. Measured on the shipped
// endpoint (POST /rest/api/3/search/jql, real Jira Cloud, anonymous, negative-controlled first —
// scripts/w34-jira-api-adf-probe.py), over 2,000 issues of one real project, 1,828 of which have a
// description at all:
//
//	inlineCard — a bare URL                          753
//	media / mediaInline — an attachment                90
//	mention                                            34
//	emoji                                              11
//	descriptions carrying at least one of these       587 of 1,828 (32.1%)
//	descriptions with NO `text` node at all — they
//	  flattened to the empty string                     6 of 1,828
//
// ⚠ THE UNIT IS "descriptions", NOT "issues", AND THE TWO DIFFER BY 172 HERE. A percentage of issues
// would have counted every description-less issue as unaffected and read as 29.4%. The denominator
// is a fact about the query before it is one about the product.
//
// The prose that survives reads as broken: "Follow up to   - remove the deprecated stuff",
// "These have been deprecated since 6.2 (see  )." — both verbatim from real issues, and in both the
// only thing that says WHAT was deprecated or followed up is the link that vanished.
//
// ⚠ AND THE LOSS IS NOT ONLY ON THE PAGE. `description` is the product's ONLY full-text index —
// issue.Store.Search is `to_tsvector('english', title || ' ' || description) @@ websearch_to_tsquery`
// — so an issue whose distinguishing content is a link is unfindable by that link, and the import
// reports {imported:N, warnings:[]}.

// adfAttrText pins, PER NODE TYPE, the attribute holding the text to emit.
//
// ⚠ EVERY ENTRY IS JIRA'S OWN ANSWER, NOT A CHOICE MADE HERE, and that is what licenses writing a
// value into a user's description rather than reporting it. `expand: renderedFields` on the same
// endpoint returns Atlassian's own HTML rendering of the same document, and it emits exactly the
// attribute named below:
//
//	inlineCard {"url":"https://…/browse/HHH-20738"} → <a href="…">https://…/browse/HHH-20738</a>
//	                                                  the URL IS the link text Jira shows
//	emoji      {"shortName":":smiley:","text":"😃"}  → … such a request 😃
//	mention    {"id":"557058:aafa…","text":"@Steve Ebersole"}
//	                                                → <a class="user-hover" …>Steve Ebersole</a>
//
// ⚠ THE mention ROW IS THE ONE THAT IS NOT VERBATIM, AND A CONTROL FOUND IT RATHER THAN A READING.
// The probe's first draft asserted "the rendered HTML contains attrs.text" for all three and mention
// FAILED on HHH-20539 — Atlassian DROPS the leading "@", because the HTML renders a chip and the
// sigil is the chip. This importer writes attrs.text WITH the "@", which is a decision and is stated
// as one: in plain text, inside a column that is also the search index, "@Steve Ebersole" is what the
// author typed and is what marks the string as a person rather than as prose. The control was made
// precise (RENDERED_AS in the probe) rather than loosened into a substring match that would have
// passed on anything.
//
// ⚠ IT IS A PINNED LIST FOR jiraTimeLayouts' REASON: a type this environment has never seen
// serialised is a type whose attribute nobody here can name. `date` and `status` (the lozenge) both
// carry attrs.text per the ADF spec and NEITHER OCCURS ONCE in the measured project, so neither is
// listed — the probe FAILS on an unpinned attrs-borne type rather than this file guessing one.
var adfAttrText = map[string]string{
	"inlineCard": "url",
	"mention":    "text",
	"emoji":      "text",
}

// adfNoTextEquivalent pins the node types measured to carry real content for which Jira's own
// rendering produces NO text at all: an attachment renders as <img> or as a download link. There is
// nothing to place, so these are REPORTED — a deliberate drop nobody is told about is
// indistinguishable from the silent ones #71/#72/#73/#83/#87 each found one field over.
var adfNoTextEquivalent = map[string]struct{}{
	"media":       {},
	"mediaInline": {},
}

// ⚠ WHY THIS IS TWO PINNED TABLES AND NOT ONE RULE, AND THE ALTERNATIVE WAS MEASURED BEFORE IT WAS
// REJECTED. The obvious general rule is "report any leaf node that carries attrs" — it needs no
// table and it survives node types nobody has seen. It is also WRONG: across the same 2,000 issues
// an EMPTY `paragraph` carries attrs.localId 88 times, `rule` 17 times and an empty `heading`
// {level, localId} 4 times. localId is editor identity, not content, so that rule manufactures 109
// warnings over those 2,000 issues about nothing lost at all — and a warning channel that cries wolf is the
// channel an operator stops reading, which costs every other note kind in csv.go. Anything in
// neither table stays silent, exactly as it is today; the probe is what notices a new type.

// adfAttrString returns the string held at key, or "" if it is absent or is not a JSON string. A
// non-string where text is expected is not a shape to invent a rendering for — it falls through to
// the dropped-node report, which names the type.
func adfAttrString(attrs map[string]json.RawMessage, key string) string {
	raw, ok := attrs[key]
	if !ok {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) != nil {
		return ""
	}
	return s
}

// droppedTypes is an ORDERED set of node types whose payload could not be placed.
//
// ⚠ ORDERED AND DEDUPLICATED, AND BOTH HALVES ARE LOAD-BEARING. The pipeline COUNTS notes
// (map[FieldNote]int), so three attachments on ONE issue must contribute ONE note or the warning
// reads "3 issue(s)" about a single issue. And document order makes the same import produce the same
// warnings every run, which map iteration would not.
type droppedTypes struct {
	order []string
	seen  map[string]bool
}

func (d *droppedTypes) add(nodeType string) {
	if d.seen == nil {
		d.seen = map[string]bool{}
	}
	if d.seen[nodeType] {
		return
	}
	d.seen[nodeType] = true
	d.order = append(d.order, nodeType)
}

// notes renders the set as one FieldNote per type. Field is `description` so the rendered line names
// the field an operator will go and look at.
func (d *droppedTypes) notes() []FieldNote {
	if len(d.order) == 0 {
		return nil
	}
	out := make([]FieldNote, 0, len(d.order))
	for _, nodeType := range d.order {
		out = append(out, FieldNote{Field: fieldDescription, Value: nodeType, Via: viaADFNodeDropped})
	}
	return out
}

// fieldDescription names the field in a rendered warning. It is a separate constant from the date
// fields for their reason: the line has to send the operator to the right place in their editor.
const fieldDescription = "description"

// viaADFNodeDropped — an ADF node carried content and this importer placed none of it. Distinct from
// every other Via because it is the only one about a value that was never a scalar at all.
const viaADFNodeDropped = "adf-node-dropped"
