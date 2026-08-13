package analytics_test

// THE CSV EXPORT ANSWERS 200 OK WITH AN EMPTY FILE WHEN IT FAILS, INCLUDING ON THE EXACT INPUTS ITS
// OWN JSON SIBLINGS REFUSE WITH 400.
//
// analytics.Handler.Export commits the status line and the download headers BEFORE it computes the
// report, then discards every error the engine returns (`_ = h.engine.Export…CSV(...)`). MEASURED at
// 6e2906f by driving the handler:
//
//	report=distribution&group_by=bogus  -> 200, text/csv, attachment; filename="distribution-….csv", 0 bytes
//	report=velocity (no team_id)        -> 200, text/csv, attachment; …,                 header row only
//	report=nosuchreport                 -> 200, text/csv, attachment; filename="nosuchreport-….csv"
//
// while GET …/analytics/distribution?group_by=bogus returns 400 DISTRIBUTION_FAILED with the reason
// and GET …/analytics/velocity with no team_id returns 400 MISSING_TEAM. Same engine call, same bad
// input, two answers — and the export's answer is a successful download of nothing.
//
// ⚠ THE `default:` ARM IS THE EVIDENCE THAT THE ORDERING WAS KNOWN. It writes "error\nunknown
// report: …" INTO the CSV body with the comment "Headers are already committed; we can't switch to
// a JSON error response" — for the one failure that could have been detected before a byte was
// committed. The three that reach the engine got nothing.
//
// ⚠ NOTHING HAD EVER DRIVEN THIS HANDLER. Every analytics test in the repo calls the Engine
// directly; there was no test of any of the seven HTTP routes, so all three behaviours above are
// what a caller got and no gate could see it.
//
// WHAT IS PINNED HERE: a failing export is refused with the same JSON error shape the six sibling
// routes already use, and a SUCCEEDING export still streams CSV with its download headers — the
// second half is the anti-vacuity control, because "refuse everything" would satisfy the first half
// alone.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/talyvor/track/internal/analytics"
	"github.com/talyvor/track/internal/authz"
	"github.com/talyvor/track/internal/testutil"
)

// exportGet drives Handler.Export for one query string as an authorized member of wsID.
func exportGet(t *testing.T, h *analytics.Handler, wsID, query string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/v1/workspaces/"+wsID+"/analytics/export?"+query, nil)
	req = req.WithContext(authz.WithAuthorized(req.Context(), wsID, "member-1"))
	rec := httptest.NewRecorder()
	h.Export(rec, req)
	return rec
}

// assertRefused is the shape every sibling route already answers a bad request with: a non-2xx, a
// JSON body carrying a code and a reason, and NO download headers — a browser must not save this.
func assertRefused(t *testing.T, rec *httptest.ResponseRecorder, what string) {
	t.Helper()
	res := rec.Result()
	if res.StatusCode == http.StatusOK {
		t.Errorf("%s: status 200 — a failed export reported success (body %d bytes: %q)",
			what, rec.Body.Len(), rec.Body.String())
	}
	if ct := res.Header.Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Errorf("%s: Content-Type %q — a refusal must not be served as the file that was asked for", what, ct)
	}
	if cd := res.Header.Get("Content-Disposition"); cd != "" {
		t.Errorf("%s: Content-Disposition %q — a refusal must not be offered as a download", what, cd)
	}
	var body struct {
		Error string `json:"error"`
		Code  string `json:"code"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("%s: body is not the JSON error shape: %v (%q)", what, err, rec.Body.String())
	}
	if body.Code == "" || body.Error == "" {
		t.Errorf("%s: refusal carries no reason: code=%q error=%q", what, body.Code, body.Error)
	}
}

// The engine here holds a REAL pool, not a nil one: two of these three cases used to reach the
// database, and refusing them on a mock would prove nothing about the route a caller hits.
func TestExport_AFailingExportIsRefusedRatherThanServedAsAnEmptyFile(t *testing.T) {
	d := testutil.New(t)
	ws := d.Workspace(t)
	h := analytics.NewHandler(analytics.New(d.Pool))

	t.Run("unsupported group_by", func(t *testing.T) {
		// The JSON sibling answers 400 DISTRIBUTION_FAILED for this exact value.
		assertRefused(t, exportGet(t, h, ws.ID, "report=distribution&group_by=bogus"), "distribution/bogus")
	})

	t.Run("velocity without team_id", func(t *testing.T) {
		// The JSON sibling answers 400 MISSING_TEAM; the export returned a header-only CSV, which
		// reads as "this team has no cycles" rather than "you did not name a team".
		assertRefused(t, exportGet(t, h, ws.ID, "report=velocity"), "velocity/no-team")
	})

	t.Run("unknown report", func(t *testing.T) {
		assertRefused(t, exportGet(t, h, ws.ID, "report=nosuchreport"), "unknown-report")
	})
}

// THE ANTI-VACUITY HALF. A fix that refused every export would pass the test above; this one fails
// unless a legitimate export still arrives as a CSV download, and it runs against real Postgres so
// "the engine ran" is a fact rather than a mock's opinion. An EMPTY report is included on purpose:
// no rows is a successful export, not a failure, and conflating the two would be the same defect
// pointing the other way.
func TestExport_ASuccessfulExportStillStreamsItsCSVDownload(t *testing.T) {
	d := testutil.New(t)
	ws := d.Workspace(t)
	team := d.Team(t, ws.ID)
	d.Issue(t, ws.ID, team.ID)

	h := analytics.NewHandler(analytics.New(d.Pool))

	cases := []struct {
		name, query, wantHeader string
		wantRows                bool
	}{
		{"distribution has rows", "report=distribution&group_by=status", "status,count,pct,ai_cost_usd", true},
		{"velocity is empty but fine", "report=velocity&team_id=" + team.ID,
			"cycle_id,cycle_name,start_date,end_date,completed,total,completion_rate,ai_cost_usd", false},
		{"ai-costs", "report=ai-costs", "date,cost_usd,issues_worked", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := exportGet(t, h, ws.ID, c.query)
			res := rec.Result()
			if res.StatusCode != http.StatusOK {
				t.Fatalf("status %d, want 200 (body %q)", res.StatusCode, rec.Body.String())
			}
			if ct := res.Header.Get("Content-Type"); !strings.Contains(ct, "text/csv") {
				t.Errorf("Content-Type %q, want text/csv", ct)
			}
			if cd := res.Header.Get("Content-Disposition"); !strings.Contains(cd, "attachment") {
				t.Errorf("Content-Disposition %q, want an attachment", cd)
			}
			lines := strings.Split(strings.TrimRight(rec.Body.String(), "\n"), "\n")
			if len(lines) == 0 || strings.TrimRight(lines[0], "\r") != c.wantHeader {
				t.Fatalf("first CSV line = %q, want %q", rec.Body.String(), c.wantHeader)
			}
			if c.wantRows && len(lines) < 2 {
				t.Errorf("CSV carries only its header row; the seeded issue should appear: %q", rec.Body.String())
			}
		})
	}
	_ = context.Background
}
