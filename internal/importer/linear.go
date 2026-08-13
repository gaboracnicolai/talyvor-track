package importer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/talyvor/track/internal/model"
)

// linear.go — T8 Build C.3: the Linear GraphQL IssueSource.
//
// Auth is the unusual `Authorization: <API_KEY>` (NO "Bearer "). Pagination is Relay cursor
// (issues(first,after) → pageInfo{hasNextPage,endCursor}). Rate-limit is signalled by HTTP 400 with a
// top-level errors[].extensions.code == "RATELIMITED" (NOT 429); other errors can arrive on HTTP 200 in
// errors[], so a 200 is not automatically success — the client parses the body every time.

const defaultLinearURL = "https://api.linear.app/graphql"

const linearIssuesQuery = `query($teamId: String!, $after: String) {
  team(id: $teamId) {
    issues(first: 100, after: $after) {
      pageInfo { hasNextPage endCursor }
      nodes { identifier title description state { name type } priority labels { nodes { name } } dueDate completedAt createdAt updatedAt }
    }
  }
}`

// linearTimeLayouts are pinned BY HAND, in the order tried.
//
// ⚠ THE PROVENANCE IS WEAKER THAN jiraTimeLayouts' AND IS NOT DRESSED UP AS EQUAL. #74 pinned Jira's
// layouts from the BYTES a real Jira sent. What this environment can measure for Linear is the
// DECLARED SCALAR TYPE, by unauthenticated introspection:
//
//	Issue.dueDate     : TimelessDate  "The date at which the issue is due."
//	Issue.completedAt : DateTime      "The time at which the issue was moved into completed state."
//
// Both scalars' descriptions then describe what the API ACCEPTS, not what it EMITS ("Accepts
// shortcuts like `2021` ... Also accepts ISO 8601 durations"), and a coercion probe confirms that
// input space is permissive enough to say nothing about the output: `2026-08-09` and
// `2026-08-09T00:00:00Z` are BOTH accepted for a TimelessDate. So the OUTPUT SERIALISATION IS NOT
// MEASURABLE FROM HERE without a tenant.
//
// ⚠ WHICH IS WHY THE REFUSAL IS THE LOAD-BEARING PART, NOT THE LIST. A value no layout accepts is
// REPORTED, never nil'd — so a tenant whose serialisation differs from both shapes below learns it
// on its first import instead of receiving a column of nulls that reads as "we have no due dates".
// This is deliberately NOT shared with parseJiraTime: sharing would lend Jira's observed-bytes
// provenance to a field nobody here has ever seen serialised, the overclaim #75 caught in this
// package once already.
var linearTimeLayouts = []string{
	time.RFC3339, // DateTime — ISO 8601 date-time, with or without fractional seconds, Z or offset
	"2006-01-02", // TimelessDate — ISO 8601 date only; time.RFC3339 REFUSES this shape
}

// parseLinearTime returns the instant and true, or false if no pinned layout accepts the value. The
// caller REPORTS a false — it never silently nils, which is what keeps the hand-pinned list honest.
func parseLinearTime(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, false
	}
	for _, layout := range linearTimeLayouts {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC(), true
		}
	}
	return time.Time{}, false
}

// linearDueDate maps `dueDate`. Absent is not a loss and is not reported; a value in a shape no
// pinned layout accepts IS a loss, and is.
func linearDueDate(raw string) (*time.Time, []FieldNote) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	t, ok := parseLinearTime(raw)
	if !ok {
		return nil, []FieldNote{{Field: fieldDueDate, Value: raw, Via: viaUnparseableDate}}
	}
	return &t, nil
}

// linearCompletedAt maps `completedAt` and refuses it unless the issue imported as done — #74's
// decision, inherited rather than re-litigated: Track's issue.Store.Update stamps completed_at only
// on a transition ONTO done and clears it on any transition away, and analytics' resolution-stats
// query selects on `completed_at IS NOT NULL` with no status predicate, so an abandoned issue
// carrying one counts as delivered work.
//
// ⚠ THE RULE FITS LINEAR BETTER THAN IT FITS JIRA, which is worth stating rather than assuming:
// Linear's schema says completedAt is "the time at which the issue was moved into completed state"
// and gives cancellation its OWN field, `canceledAt` — unlike Jira, which resolves "Won't Do" and so
// stamps resolutiondate on cancelled work. Linear should therefore rarely send this on a non-done
// issue. "Rarely" is not "never", and the states this importer REFUSES to classify (`triage`,
// `duplicate`, and any unknown name with no type) all import as backlog — so the refusal still has
// to exist, and it is still reported rather than silent.
func linearCompletedAt(raw string, status model.IssueStatus) (*time.Time, []FieldNote) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	t, ok := parseLinearTime(raw)
	if !ok {
		return nil, []FieldNote{{Field: fieldCompletionTime, Value: raw, Via: viaUnparseableDate}}
	}
	if status != model.StatusDone {
		return nil, []FieldNote{{Field: fieldCompletionTime, Value: raw, Mapped: string(status), Via: viaStatusNotDone}}
	}
	return &t, nil
}

type linearClient struct {
	http  *http.Client
	url   string
	token string // sent verbatim as the Authorization header — NO "Bearer " prefix
	team  string
	retry retryer
}

// newLinearClient builds the Linear client. Production passes no httpc → the SSRF-guarded client; tests
// may inject a loopback-capable client (httptest binds 127.0.0.1, which the guard blocks by design).
func newLinearClient(token, teamKey, baseURL string, httpc ...*http.Client) *linearClient {
	url := baseURL
	if url == "" {
		url = defaultLinearURL
	}
	return &linearClient{
		http:  clientOrSafe(httpc),
		url:   url,
		token: token,
		team:  teamKey,
		retry: defaultRetryer(),
	}
}

type linearNode struct {
	Identifier  string `json:"identifier"`
	Title       string `json:"title"`
	Description string `json:"description"`
	State       struct {
		Name string `json:"name"`
		// Type is Linear's CANONICAL state category — the value a team cannot rename. Absent ⇒ ""
		// ⇒ today's name-only behaviour, which is what makes the query change fail-safe on the
		// decoding side. The query change itself is not fail-safe (an unknown field 400s the whole
		// document), which is why it was measured against the live schema before it was made; see
		// mapLinearStateType and scripts/w34-linear-schema-probe.py.
		Type string `json:"type"`
	} `json:"state"`
	// ⚠ json.Number, NOT int, AND IT IS THE SAME RULE THE TWO DATE FIELDS BELOW ARE DECODED UNDER:
	// a shape this importer does not expect must be REPORTED, never a decode error that fails the
	// whole page. Measured by unauthenticated introspection (scripts/w34-linear-api-priority-probe.py),
	// `Issue.priority` is declared `Float!` — a double — and `2`, `2.0` and `2e0` are all legal JSON
	// serialisations of the same value. encoding/json accepts only the first into an `int`, and
	// fetchPage returns on ANY decode error, so one such value discarded the whole 100-issue page
	// and stopped the source, abandoning every later page for a field nothing reads as a number.
	//
	// ⚠ THE OUTPUT SERIALISATION IS STILL NOT MEASURABLE FROM HERE — Linear authenticates before it
	// executes, so only a real tenant can say which spelling its server emits (W3.4 item (3)). That
	// is precisely why the decoder must not be the thing that decides: json.Number accepts every
	// spelling of the declared type and hands the verbatim bytes to the warning.
	Priority json.Number `json:"priority"`
	Labels   struct {
		Nodes []struct {
			Name string `json:"name"`
		} `json:"nodes"`
	} `json:"labels"`

	// DueDate is a TimelessDate and CompletedAt a DateTime (measured from the schema — see
	// linearTimeLayouts). Both decode as strings so an unrecognised shape can be REPORTED rather
	// than becoming a decode error that fails the whole page, and `null` decodes to "" which is the
	// absent case.
	DueDate     string `json:"dueDate"`
	CompletedAt string `json:"completedAt"`

	// ⚠ DECLARED `DateTime!` — NON_NULL — where CompletedAt is nullable (measured by unauthenticated
	// introspection; see scripts/w34-linear-api-created-probe.py). So "" here is not "this issue has
	// no opening time", which is a state Linear cannot produce; it is "the response did not come from
	// the schema this importer was written against". Reported as that, via viaNullCreatedAt.
	CreatedAt string `json:"createdAt"`

	// ⚠ ALSO `DateTime!` — NON_NULL — so "" here is not "never touched", a state Linear cannot
	// produce; it is "the response did not come from the schema this importer was written
	// against". Reported via viaNullUpdatedAt. Lands in `updated_at`, DEFAULT NOW(), which is the
	// column issue.Store.Search orders the product's main screen by.
	UpdatedAt string `json:"updatedAt"`
}

type linearResp struct {
	Data struct {
		// ⚠ A POINTER, AND THAT IS THE WHOLE OF THIS DETAIL. `team(id:)` is a NULLABLE field: a key
		// this credential cannot resolve — deleted, renamed, in another workspace, or simply not
		// visible to the token — is answered `{"data":{"team":null}}`, a 200 with NO `errors[]`.
		// As a VALUE struct this decoded to the zero value (nodes nil, hasNextPage false), which is
		// byte-for-byte what a team holding no issues looks like, so fetchPage returned an empty
		// FINAL page, the source stopped cleanly, and terminalStatus recorded the job
		// `succeeded imported=0 failed=0` with an empty warnings array. Nothing anywhere said the
		// import never had a team to read. encoding/json unmarshalling `null` into a struct is a
		// documented no-op; into a POINTER it is nil, which is the one shape that tells the two
		// states apart. See linear_null_team_test.go — (2) is the control that keeps a genuinely
		// empty team a clean success.
		Team *struct {
			Issues struct {
				PageInfo struct {
					HasNextPage bool   `json:"hasNextPage"`
					EndCursor   string `json:"endCursor"`
				} `json:"pageInfo"`
				Nodes []linearNode `json:"nodes"`
			} `json:"issues"`
		} `json:"team"`
	} `json:"data"`
	Errors []struct {
		Message    string `json:"message"`
		Extensions struct {
			Code string `json:"code"`
		} `json:"extensions"`
	} `json:"errors"`
}

type linearPage struct {
	issues  []mappedIssue
	hasNext bool
	cursor  string
}

// fetchPage issues one paginated query, retrying ONLY on a RATELIMITED response (honoring the reset header).
// A 200 carrying errors[] is a real error, not a silent empty page.
func (c *linearClient) fetchPage(ctx context.Context, after string) (linearPage, error) {
	vars := map[string]any{"teamId": c.team}
	if after != "" {
		vars["after"] = after
	}
	body, _ := json.Marshal(map[string]any{"query": linearIssuesQuery, "variables": vars})

	var lastErr error
	for attempt := 0; attempt < c.retry.attempts(); attempt++ {
		status, hdr, respBody, err := postJSON(ctx, c.http, c.url, map[string]string{"Authorization": c.token}, body)
		if err != nil {
			return linearPage{}, fmt.Errorf("linear: request: %w", err)
		}
		var parsed linearResp
		if e := json.Unmarshal(respBody, &parsed); e != nil {
			return linearPage{}, fmt.Errorf("linear: decode (http %d): %w", status, e)
		}
		// Rate-limit: HTTP 400 whose errors[] carries code=RATELIMITED → retryable, distinct signal.
		if status == http.StatusBadRequest && linearRateLimited(parsed) {
			lastErr = fmt.Errorf("linear: %w", errRateLimited)
			c.retry.wait(ctx, linearResetBackoff(hdr))
			continue
		}
		if status != http.StatusOK {
			return linearPage{}, fmt.Errorf("linear: http %d: %s", status, firstLinearError(parsed))
		}
		// A 200 with errors[] is NOT a silent success.
		if len(parsed.Errors) > 0 {
			return linearPage{}, fmt.Errorf("linear: api error: %s", firstLinearError(parsed))
		}
		// A NULL TEAM IS NOT AN EMPTY TEAM. Checked AFTER the errors[] arm on purpose: a 200 that
		// carries a GraphQL error has a more specific sentence and must keep it. Reaching here with
		// a nil Team means the document was well-formed, the server raised nothing, and the field
		// the whole query hangs off resolved to null — so there is no connection to page and no
		// issue this import could ever have read. Returning an error puts it in run()'s Errors,
		// which is what stops terminalStatus calling the job succeeded.
		//
		// ⚠ THE KEY IS QUOTED BECAUSE THE KEY IS THE THING THE OPERATOR CAN CHANGE. `imported=0`
		// sends them to their backlog; this sends them to the team field on the integration —
		// which is also where W3.4's open key-vs-UUID question would surface if that is the cause.
		if parsed.Data.Team == nil {
			return linearPage{}, fmt.Errorf(
				"the team %q did not resolve — Linear answered data.team = null with no errors[], so this "+
					"credential can see no team under that id/key and NOTHING was imported; check the team "+
					"key on the Linear integration", c.team)
		}
		iss := parsed.Data.Team.Issues
		return linearPage{issues: mapLinearNodes(iss.Nodes), hasNext: iss.PageInfo.HasNextPage, cursor: iss.PageInfo.EndCursor}, nil
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("linear: %w (retries exhausted)", errRateLimited)
	}
	return linearPage{}, lastErr
}

func linearRateLimited(r linearResp) bool {
	for _, e := range r.Errors {
		if e.Extensions.Code == "RATELIMITED" {
			return true
		}
	}
	return false
}

func firstLinearError(r linearResp) string {
	if len(r.Errors) > 0 {
		return r.Errors[0].Message
	}
	return "unknown error"
}

// linearResetBackoff derives a wait from X-RateLimit-Requests-Reset (epoch ms). Absent/invalid ⇒ 1s.
func linearResetBackoff(h http.Header) time.Duration {
	for _, key := range []string{"X-RateLimit-Requests-Reset", "X-RateLimit-Complexity-Reset"} {
		if v := h.Get(key); v != "" {
			if ms, err := strconv.ParseInt(v, 10, 64); err == nil {
				d := time.Until(time.UnixMilli(ms))
				if d > 0 {
					return d
				}
			}
		}
	}
	return time.Second
}

func mapLinearNodes(nodes []linearNode) []mappedIssue {
	out := make([]mappedIssue, 0, len(nodes))
	for _, n := range nodes {
		labels := make([]string, 0, len(n.Labels.Nodes))
		for _, l := range n.Labels.Nodes {
			labels = append(labels, l.Name)
		}
		status, statusOK := mapLinearStatus(n.State.Name)
		var fallback statusFallback
		if !statusOK {
			status, fallback = resolveLinearStateType(n.State.Type, status)
		}
		prio, prioOK := linearPriorityFromNumber(n.Priority)
		due, dueNotes := linearDueDate(n.DueDate)
		completed, completedNotes := linearCompletedAt(n.CompletedAt, status)
		created, createdNotes := linearAPICreated(n.CreatedAt)
		updated, updatedNotes := linearAPIUpdated(n.UpdatedAt)
		out = append(out, mappedIssue{
			issue: model.Issue{
				Identifier:  n.Identifier, // the provider-key (ENG-123) — what C.2's upsert + PR #30 resolve on
				Title:       n.Title,
				Description: n.Description,
				Status:      status,
				Priority:    prio,
				Labels:      labels,
				DueDate:     due,
				CompletedAt: completed,
				CreatedAt:   created,
				UpdatedAt:   updated,
			},
			// The priority reaches the warning as the PROVIDER'S OWN BYTES. strconv.Itoa could only
			// ever render what the int decode produced, which for every shape this change exists to
			// read is 0 — a warning naming a value the response never carried.
			notes: append(collectNotes(n.State.Name, status, statusOK, fallback, string(n.Priority), prio, prioOK),
				append(dueNotes, append(completedNotes, append(createdNotes, updatedNotes...)...)...)...),
		})
	}
	return out
}

// linearPriorityFromNumber maps Linear's numeric priority (0 none, 1 urgent, 2 high, 3 medium/normal,
// 4 low) to Track's scale, and reports whether the value was ON that scale. 0 is a REAL value the
// user chose ("No priority"), so it is recognised; anything outside 0..4 is not a Linear priority and
// falls back to none — reported rather than assumed.
//
// ⚠ AN ABSENT priority IS STILL RECOGNISED, AND THAT IS DELIBERATELY UNCHANGED. `Float!` is NON_NULL,
// so an absent value means the response did not come from the schema this importer was written
// against — the same argument createdAt/updatedAt make for reporting it. It is NOT reported here,
// because today an absent priority decodes to the int zero and imports as PriorityNone with no note,
// and turning that into a warning is a separate decision about a separate field state. This change
// is about a value that ARRIVES in a spelling the decoder refused; smuggling the absence question in
// under it would make both harder to argue with. It is written down instead: W3.4.
//
// ⚠ NON-INTEGRAL IS OFF THE SCALE, NOT ROUNDED. Linear's own field description enumerates 0..4, so
// 2.5 is not "High-ish" — it is a value this importer cannot place, and the existing warning channel
// already says exactly that, now carrying the provider's verbatim bytes rather than an int.
func linearPriorityFromNumber(raw json.Number) (model.IssuePriority, bool) {
	f, err := raw.Float64()
	if err != nil {
		// Absent or unparseable. Byte-identical to the pre-json.Number behaviour, where an absent
		// priority decoded to the int zero: PriorityNone, recognised, no note.
		return model.PriorityNone, true
	}
	p := int(f)
	if float64(p) != f {
		return model.PriorityNone, false
	}
	switch p {
	case 0:
		return model.PriorityNone, true
	case 1:
		return model.PriorityUrgent, true
	case 2:
		return model.PriorityHigh, true
	case 3:
		return model.PriorityMedium, true
	case 4:
		return model.PriorityLow, true
	default:
		return model.PriorityNone, false
	}
}

// resolveLinearStateType is the second chance an unrecognised Linear state NAME gets. It never runs
// for a name mapLinearStatus knows, so a recognised import is byte-for-byte what it was.
//
// It returns the status to use plus the note material describing WHICH of the three things happened,
// because a type that never arrived must not be reportable as one that arrived and resolved — that is
// the only way a real tenant's first import can tell anyone whether this code executed. Exactly
// #73's argument for the Jira half; the failure shape is the provider-independent one.
func resolveLinearStateType(typ string, unresolved model.IssueStatus) (model.IssueStatus, statusFallback) {
	if strings.TrimSpace(typ) == "" {
		return unresolved, statusFallback{via: viaNoStateType}
	}
	mapped, ok := mapLinearStateType(typ)
	if !ok {
		return unresolved, statusFallback{via: viaStateType, value: typ}
	}
	return mapped, statusFallback{via: viaStateType, value: typ, resolved: true}
}

// linearSource drains the Linear cursor pagination behind Next() — the seam pattern from Build A: buffer a
// page, yield its issues one by one, fetch the next page on exhaustion. A fetch failure is surfaced ONCE as a
// SourceRow.Err (so run() records it and the job ends partial/failed) and then the source stops — NEVER a
// silent stop that would look like a complete import.
type linearSource struct {
	// ctx is the IMPORT's context — the one runner.execute holds and SIGTERM cancels. It lives on
	// the struct because IssueSource.Next() takes no parameters; see provider_context_test.go for
	// the alternative (Next(ctx) on the interface) and why it was not taken. Without it Next
	// fetched on context.Background(), and MEASURED at 38287be a provider kept serving a request
	// for the full 3s after the caller's context died — no context the caller held could stop it.
	ctx    context.Context
	client *linearClient
	buf    []mappedIssue
	pos    int
	cursor string
	// exhausted is "the provider has no more pages" — Relay's pageInfo.hasNextPage, which unlike
	// Jira's isLast is a non-null field of the connection and IS the terminator. What it does not
	// carry is a guarantee that a non-final page has ROWS: an empty `nodes` beside
	// hasNextPage:true is legal, and treating it as the end abandoned every later page while the
	// job recorded `succeeded imported=0`. See api_pagination_termination_test.go (3).
	exhausted bool
	// stalled is the twin of jiraSource.stalled, for the same reason and with the same shape: a page
	// that carried rows and handed back the endCursor it was given. Flag rather than immediate
	// return so the page's own rows are yielded first — exactly what the empty-endCursor branch
	// below already does, and for the same reason.
	stalled    bool
	emptyPages int
	started    bool
	done       bool
	rowNum     int
}

func newLinearSource(ctx context.Context, token, teamKey, baseURL string, httpc ...*http.Client) *linearSource {
	return &linearSource{ctx: ctx, client: newLinearClient(token, teamKey, baseURL, httpc...)}
}

func (s *linearSource) Next() (SourceRow, bool) {
	if s.done {
		return SourceRow{}, false
	}
	// A LOOP, NOT AN `if` — the twin of jiraSource.Next, for the same reason and with the same
	// three outcomes. An empty page is not the end of the import; a provider that cannot be paged
	// further is an ERROR ROW, never a silent stop and never an unbounded walk.
	for s.pos >= len(s.buf) {
		if s.started && s.exhausted {
			s.done = true
			return SourceRow{}, false // clean exhaustion
		}
		// hasNextPage WITHOUT an endCursor is unfetchable: fetchPage("") omits `after` and Linear
		// answers with the FIRST page, so continuing re-reads the connection from the top for ever
		// (measured at be7b2cf: the same issue yielded 12 times and rising) while stopping quietly
		// would call a truncated import complete. The third answer is to SAY it — after this page's
		// own rows have been yielded, which are real whatever the cursor says.
		if s.started && s.cursor == "" {
			s.done = true
			return SourceRow{RowNum: s.rowNum + 1, Err: errors.New(
				"linear: fetch page: pageInfo reports hasNextPage with an empty endCursor — there is no cursor to " +
					"request the next page with, so this import is INCOMPLETE")}, true
		}
		// A cursor that is PRESENT but unchanged — the twin of the branch above, and of jiraSource's.
		// Ordered after it so the more specific "there is no cursor at all" keeps its own sentence.
		// MEASURED at 403e32b6: 40 rows in 40 HTTP calls and rising, all the same issue, no error row.
		if s.started && s.stalled {
			s.done = true
			return SourceRow{RowNum: s.rowNum + 1, Err: fmt.Errorf(
				"linear: fetch page: no progress — the provider returned the same endCursor (%q) it was given "+
					"while pageInfo reported another page, so the import stopped rather than re-request it for "+
					"ever; this import is INCOMPLETE", s.cursor)}, true
		}
		prevCursor := s.cursor
		page, err := s.client.fetchPage(s.ctx, s.cursor)
		if err != nil {
			s.done = true
			return SourceRow{RowNum: s.rowNum + 1, Err: fmt.Errorf("linear: fetch page: %w", err)}, true
		}
		s.started, s.buf, s.pos, s.cursor = true, page.issues, 0, page.cursor
		s.exhausted = !page.hasNext
		// Recorded BEFORE the break so it survives the rows being yielded. The empty-cursor case is
		// excluded here because the branch above owns it and says something more precise.
		//
		// ⚠ THE EXCLUSION IS BELT-AND-BRACES, AND THE MEASUREMENT SAYS SO. Dropping it (control C5)
		// is green, because the empty-cursor branch above answers first; swapping the two branches
		// (C6) is green, because this term makes them mutually exclusive. Only removing BOTH (C7)
		// can route a no-cursor page into this report — and C7 IS GREEN TOO, which is a fact about
		// the tests rather than about the code: the sentence below names `endCursor`, and
		// TestLinearSource_HasNextPageWithNoCursorIsReportedNotSwallowed discriminates on exactly
		// that word, so it cannot tell the two refusals apart. Neither message is wrong about its
		// case and no caller branches on which one it got, so this is recorded rather than repaired.
		if !s.exhausted && page.cursor != "" && page.cursor == prevCursor {
			s.stalled = true
		}
		if len(page.issues) > 0 {
			s.emptyPages = 0
			break
		}
		if s.exhausted {
			continue // an empty LAST page: the loop's own guard ends it cleanly on the next pass
		}
		if page.cursor == prevCursor {
			s.done = true
			return SourceRow{RowNum: s.rowNum + 1, Err: fmt.Errorf(
				"linear: fetch page: no progress — the provider returned an empty page and the same endCursor (%q), "+
					"so the import stopped rather than re-request it for ever", prevCursor)}, true
		}
		s.emptyPages++
		if s.emptyPages > maxConsecutiveEmptyPages {
			s.done = true
			return SourceRow{RowNum: s.rowNum + 1, Err: fmt.Errorf(
				"linear: fetch page: %d consecutive pages carried no issues while the provider reported more pages — "+
					"stopping rather than paging for ever; this import is INCOMPLETE", s.emptyPages)}, true
		}
	}
	m := s.buf[s.pos]
	s.pos++
	s.rowNum++
	return SourceRow{Issue: m.issue, RowNum: s.rowNum, Notes: m.notes}, true
}
