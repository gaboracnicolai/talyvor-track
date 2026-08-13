package importer

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/talyvor/track/internal/issue"
	"github.com/talyvor/track/internal/testutil"
)

// adf_attrs_job_test.go — the ADF flattener driven END TO END through the async runner onto real
// Postgres, and then read back THROUGH THE QUERY THAT CANNOT FIND WHAT IT DROPPED.
//
// ⚠ THIS IS NOT THE MAPPER TEST TWICE. issue.Store.Search is
// `to_tsvector('english', title || ' ' || description) @@ websearch_to_tsquery(...)`, so the
// description is not merely displayed (IssueDetail.tsx:112 renders it verbatim) — it is the
// product's ONLY full-text index. A link the flattener drops is not just missing from a page, it is
// missing from every search, and a column assertion cannot see that.
//
// ⚠ THE QUERY TERM IS A HOST, NOT AN ISSUE KEY, AND THAT WAS MEASURED RATHER THAN CHOSEN. Postgres'
// default parser splits a URL into `host` and `url_path` tokens:
//
//	to_tsvector('english','… https://hibernate.atlassian.net/browse/HHH-20738 …')
//	  → 'hibernate.atlassian.net':5 'hibernate.atlassian.net/browse/hhh-20738':4 '/browse/hhh-20738':6
//
// so websearch_to_tsquery('HHH-20738') does NOT match the URL even once it is in the column. A test
// written on the obvious term would have stayed RED after a correct fix and been "fixed" by
// weakening it. The host matches, and "which issues link to this host" is the query an operator
// actually runs.
//
// ⚠ AND THE INSTRUMENT CARRIES ITS OWN POSITIVE CONTROL INSIDE THE ASSERTION. PROJ-CONTROL holds the
// same host as ORDINARY TEXT, so it is found BEFORE this merge and after it. If the search ever
// stops reading descriptions at all, both rows disappear together and the test says so — a red on
// PROJ-LINK alone is the product defect, a red on both is a broken instrument.

const adfSearchHost = "hibernate.zulipchat.com"

func adfJobIssue(key, adf string) string {
	return `{"key":"` + key + `","fields":{"summary":"imported","description":` + adf +
		`,"status":{"name":"Done"},"priority":{"name":"Medium"},"labels":[]}}`
}

func TestJobRow_JiraAPI_ALinkedURLIsInThePostgresSearchIndex(t *testing.T) {
	d := testutil.New(t)
	ctx := context.Background()
	ws := d.Workspace(t)
	team := d.Team(t, ws.ID)

	srv := httptest.NewServer(cannedPages([]string{jiraAPIPage(
		// The defect: the host reaches Postgres only if the inlineCard's attrs.url does.
		adfJobIssue("PROJ-LINK", `{"type":"doc","version":1,"content":[{"type":"paragraph","content":[
			{"type":"text","text":"Follow up from the discussion here: "},
			{"type":"inlineCard","attrs":{"url":"https://`+adfSearchHost+`/channel/132096/topic/round","localId":"e6af"}}]}]}`),
		// The instrument's positive control: the same host as plain text, indexed either way.
		adfJobIssue("PROJ-CONTROL", `{"type":"doc","version":1,"content":[{"type":"paragraph","content":[
			{"type":"text","text":"discussed on `+adfSearchHost+` last week"}]}]}`),
		// blockCard — inlineCard's BLOCK-LEVEL twin, and the type the probe's 2,000-issue bound hid.
		// Its description is a linked card and NOTHING else, so before the pin this row's
		// `description` column is the EMPTY STRING: not merely unfindable by the host, carrying no
		// text at all, with no note saying so. Measured on HHH-18501 of the same real instance.
		adfJobIssue("PROJ-CARD", `{"type":"doc","version":1,"content":[
			{"type":"blockCard","attrs":{"url":"https://`+adfSearchHost+`/channel/132096/topic/card"}}]}`),
		// The reported half: an attachment has no text equivalent, so it is named in the job row.
		adfJobIssue("PROJ-SHOT", `{"type":"doc","version":1,"content":[
			{"type":"paragraph","content":[{"type":"text","text":"see the screenshot"}]},
			{"type":"mediaSingle","content":[{"type":"media","attrs":{"type":"file","id":"51029fdf",
				"alt":"Screenshot 2026-05-27.png","collection":"","height":725,"width":568}}]}]}`),
	)}, jiraAPIPage()))
	defer srv.Close()

	istore := testIntegrationStore(t, d)
	if _, err := istore.Upsert(ctx, ws.ID, "jira", "me@corp.com:api-token", "PROJ", srv.URL); err != nil {
		t.Fatal(err)
	}
	jobID := insertAPIJob(t, d, ws.ID, team.ID, "jira_api")

	istoreIssues := issue.NewStore(d.Pool)
	runner := NewRunner(NewJobStore(d.Pool), New(istoreIssues)).
		WithProviderConfig(istore).WithHTTPClient(srv.Client())
	if did, err := runner.RunOnce(ctx); err != nil || !did {
		t.Fatalf("RunOnce did=%v err=%v", did, err)
	}

	j, err := NewJobStore(d.Pool).Get(ctx, jobID)
	if err != nil {
		t.Fatal(err)
	}
	// Every row LANDS. Reporting a lost attachment is not a failed row.
	if j.Status != JobSucceeded || j.Imported != 4 || j.Failed != 0 || j.Skipped != 0 {
		t.Fatalf("job row = {status:%q imported:%d failed:%d skipped:%d}, want {succeeded 4 0 0}",
			j.Status, j.Imported, j.Failed, j.Skipped)
	}

	// ⚠ THE CONSUMER.
	found, err := istoreIssues.Search(ctx, ws.ID, adfSearchHost, 25)
	if err != nil {
		t.Fatal(err)
	}
	hit := map[string]bool{}
	for _, is := range found {
		hit[is.Identifier] = true
	}
	if !hit["PROJ-CONTROL"] {
		t.Fatalf("the CONTROL row is not in the search results either (%v) — this test's instrument "+
			"is broken, not the product: issue.Store.Search is not reading descriptions at all", found)
	}
	// ⚠ THE SAME CONSUMER, ONE NODE TYPE OVER, AND THE LOSS HERE IS TOTAL RATHER THAN PARTIAL.
	// PROJ-LINK keeps its prose when the link vanishes; PROJ-CARD keeps nothing — its whole
	// description is the card. An empty description is indistinguishable from an issue that never
	// had one, so no reader downstream can tell this row lost anything.
	if !hit["PROJ-CARD"] {
		t.Errorf("searching %q found %d issue(s) and PROJ-CARD is not among them.\n"+
			"Its ENTIRE description is an ADF `blockCard` whose whole payload is attrs.url. "+
			"Unpinned, the flattener emits nothing for it, `description` lands EMPTY, to_tsvector "+
			"indexes an empty string, and the job reports {imported:N, warnings:[]} — the issue is "+
			"unfindable and nothing anywhere says a value was dropped.", adfSearchHost, len(found))
	}
	if !hit["PROJ-LINK"] {
		t.Errorf("searching %q found %d issue(s) and PROJ-LINK is not among them.\n"+
			"Its description is prose plus a link, and the link is an ADF `inlineCard` whose whole "+
			"payload is attrs.url. The flattener reads only `text` nodes, so nothing about that URL "+
			"ever reaches `description` — and to_tsvector indexes only what is in the column. The "+
			"issue is unfindable by the one thing that distinguishes it, and the job reported "+
			"{imported:3, warnings about nothing}.", adfSearchHost, len(found))
	}

	// The warning reaches the JOB ROW's TEXT[] — 0026's channel is the one a real import is read
	// through, and a report that stops at ImportResult is inert exactly there.
	//
	// ⚠ IT ASSERTS THE SENTENCE, NOT MERELY THAT THE WORDS APPEAR, AND A CONTROL IS WHY. Deleting
	// the viaADFNodeDropped branch in FieldNote.render drops the line into the DEFAULT branch, which
	// emits `unrecognised description "media" on 1 issue(s) — imported as ""`. That string contains
	// `"media"` and `1 issue(s)` too, so the obvious assertion stays GREEN on a mutation that
	// removes the only sentence an operator can act on — #87's lesson, one field over.
	var said bool
	for _, w := range j.Warnings {
		if strings.Contains(w, `"media"`) && strings.Contains(w, "1 issue(s)") &&
			strings.Contains(w, "carries no text") && strings.Contains(w, "search reads only the text") {
			said = true
		}
	}
	if !said {
		t.Errorf("job warnings say nothing about the attachment that did not come across: %#v.\n"+
			"An attachment has no text equivalent in Jira's own rendering, so it cannot be placed — "+
			"which makes saying so the whole of what this importer can honestly do about it.", j.Warnings)
	}
}
