package lensintegration

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/talyvor/track/internal/authz"
	"github.com/talyvor/track/internal/model"
)

// unattributed_test.go — the syncer must record what it cannot attribute, and both AI-cost
// endpoints must say that their per-issue figures are a subset.
//
// See internal/issue/unattributed_spend_realpg_test.go for the full statement of what is
// skipped and why. In short: all three skips are correct, and the defect was that the money
// they skipped existed only in one slog line.

// ---- fakes ----

type recordedSpend struct {
	requestID, feature string
	cost               float64
	tokens             int
}

// ledgerUpdater mimics the real store: a feature matching a known identifier resolves; anything
// else (including "") does not. Both land a ledger row exactly once per request_id.
type ledgerUpdater struct {
	known    map[string]bool // identifiers that resolve
	seen     map[string]bool // request_ids already landed
	recorded []recordedSpend
	failOn   string // request_id that returns an error
}

func newLedgerUpdater(known ...string) *ledgerUpdater {
	f := &ledgerUpdater{known: map[string]bool{}, seen: map[string]bool{}}
	for _, k := range known {
		f.known[k] = true
	}
	return f
}

func (f *ledgerUpdater) RecordRequestSpend(_ context.Context, requestID, feature string, cost float64, tokens int, _ string) (bool, bool, error) {
	if requestID == f.failOn {
		return false, false, errors.New("boom")
	}
	if requestID == "" {
		return false, false, errors.New("issue: RecordRequestSpend requires request_id, workspace_id")
	}
	if f.seen[requestID] {
		return f.known[feature], false, nil
	}
	f.seen[requestID] = true
	f.recorded = append(f.recorded, recordedSpend{requestID, feature, cost, tokens})
	return f.known[feature], true, nil
}

func (f *ledgerUpdater) unattributed() (cost float64, n int) {
	for _, r := range f.recorded {
		if !f.known[r.feature] {
			cost += r.cost
			n++
		}
	}
	return cost, n
}

// stubByRequest serves `rows` in the {rows, next_cursor} envelope the by-request endpoint
// returns, on every path — the client only ever calls one.
func stubByRequest(t *testing.T, rows []RequestSpend) *httptest.Server {
	t.Helper()
	body, err := json.Marshal(map[string]any{"rows": rows, "next_cursor": ""})
	if err != nil {
		t.Fatalf("stub encode: %v", err)
	}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
}

// ---- syncer ----

// RED: rows whose feature resolves to no issue were dropped without a durable record —
// SyncFeatureSpend `continue`d before RecordRequestSpend on an empty feature, and discarded
// the !resolved case. GREEN: every dedupable row reaches the ledger.
func TestSyncer_UnattributedRows_ReachTheLedger(t *testing.T) {
	rows := []RequestSpend{
		{RequestID: "r1", Feature: "ENG-1", CostUSD: 10, InputTokens: 100, OutputTokens: 50},
		{RequestID: "r2", Feature: "", CostUSD: 2.5, InputTokens: 10},      // untagged
		{RequestID: "r3", Feature: "GONE-9", CostUSD: 1.5, InputTokens: 5}, // no such issue
	}
	srv := stubByRequest(t, rows)
	defer srv.Close()

	upd := newLedgerUpdater("ENG-1")
	s := NewSyncer(New(srv.URL, "k"), upd, &fakeWorkspaces{ids: []string{"ws-1"}})
	if err := s.SyncFeatureSpend(context.Background(), "ws-1"); err != nil {
		t.Fatalf("SyncFeatureSpend: %v", err)
	}

	if len(upd.recorded) != 3 {
		t.Fatalf("recorded %d rows, want 3 — untagged and unresolved spend must still be "+
			"written to the ledger, not dropped: %+v", len(upd.recorded), upd.recorded)
	}
	cost, n := upd.unattributed()
	if cost != 4.0 || n != 2 {
		t.Errorf("unattributed = ($%v, %d), want ($4.00, 2)", cost, n)
	}
}

// A row with no request_id has no dedup key, so it must NOT be handed to the store — the
// re-pulled window would multiply it. It is counted separately instead.
func TestSyncer_RowWithoutRequestID_IsNotWritten(t *testing.T) {
	rows := []RequestSpend{
		{RequestID: "", Feature: "ENG-1", CostUSD: 7.0},
		{RequestID: "r1", Feature: "ENG-1", CostUSD: 1.0},
	}
	srv := stubByRequest(t, rows)
	defer srv.Close()

	upd := newLedgerUpdater("ENG-1")
	s := NewSyncer(New(srv.URL, "k"), upd, &fakeWorkspaces{ids: []string{"ws-1"}})
	if err := s.SyncFeatureSpend(context.Background(), "ws-1"); err != nil {
		t.Fatalf("SyncFeatureSpend: %v", err)
	}

	for _, r := range upd.recorded {
		if r.requestID == "" {
			t.Errorf("a row with no request_id was written — it cannot be deduplicated")
		}
	}
	if len(upd.recorded) != 1 {
		t.Errorf("recorded %d rows, want 1 (only the dedupable one): %+v", len(upd.recorded), upd.recorded)
	}
}

// Re-pulling the same window must not re-record anything. The syncer re-reads the last 24h
// every 15 minutes.
func TestSyncer_RepulledWindow_RecordsOnce(t *testing.T) {
	rows := []RequestSpend{
		{RequestID: "r1", Feature: "", CostUSD: 3.0},
		{RequestID: "r2", Feature: "ENG-1", CostUSD: 4.0},
	}
	srv := stubByRequest(t, rows)
	defer srv.Close()

	upd := newLedgerUpdater("ENG-1")
	s := NewSyncer(New(srv.URL, "k"), upd, &fakeWorkspaces{ids: []string{"ws-1"}})
	for i := 0; i < 4; i++ {
		if err := s.SyncFeatureSpend(context.Background(), "ws-1"); err != nil {
			t.Fatalf("pull %d: %v", i, err)
		}
	}
	if len(upd.recorded) != 2 {
		t.Errorf("recorded %d rows after 4 pulls, want 2", len(upd.recorded))
	}
}

// ---- handler ----

type stubUnattributed struct {
	stubIssues
	cost float64
	n    int
	err  error
}

func (s stubUnattributed) UnattributedSpend(context.Context, string) (float64, int, error) {
	return s.cost, s.n, s.err
}

func wsReq(t *testing.T, ws string) *http.Request {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/workspaces/"+ws+"/ai-costs", nil)
	return req.WithContext(authz.WithAuthorized(req.Context(), ws, "m"))
}

// RED: the workspace rollup put Lens's real `summary` next to Track's attributed
// `top_issues` with nothing saying they do not reconcile. GREEN: unattributed spend is a
// first-class field.
func TestGetAICosts_ReportsUnattributedSpend(t *testing.T) {
	h := NewHandler(New("", ""), stubUnattributed{
		stubIssues: stubIssues{issue: &model.Issue{ID: "i", WorkspaceID: "ws-A"}},
		cost:       12.34, n: 7,
	})

	rr := httptest.NewRecorder()
	h.GetAICosts(rr, wsReq(t, "ws-A"))

	body := decodeBody(t, rr)
	un, ok := body["unattributed"].(map[string]any)
	if !ok {
		t.Fatalf("no `unattributed` block — per-issue totals cannot be reconciled against the "+
			"Lens invoice without it: %s", rr.Body.String())
	}
	if un["cost_usd"] != 12.34 {
		t.Errorf("unattributed.cost_usd = %#v, want 12.34", un["cost_usd"])
	}
	if un["requests"] != float64(7) {
		t.Errorf("unattributed.requests = %#v, want 7", un["requests"])
	}
}

// The per-issue response must say that its figure covers only issue-attributed spend, and
// name the workspace total that did not reach any issue. Without it, ai_cost_usd reads as
// the whole story — which is exactly how the frontend renders it.
func TestGetIssueAICosts_SaysTheFigureIsAttributedOnly(t *testing.T) {
	h := NewHandler(New("", ""), stubUnattributed{
		stubIssues: stubIssues{issue: &model.Issue{
			ID: "iss-1", Identifier: "A-1", WorkspaceID: "ws-A", AICostUSD: 3.0,
		}},
		cost: 5.5, n: 4,
	})

	rr := httptest.NewRecorder()
	h.GetIssueAICosts(rr, issueReq(t, "ws-A"))

	body := decodeBody(t, rr)
	un, ok := body["workspace_unattributed"].(map[string]any)
	if !ok {
		t.Fatalf("no `workspace_unattributed` block — ai_cost_usd is presented as complete "+
			"and is not: %s", rr.Body.String())
	}
	if un["cost_usd"] != 5.5 {
		t.Errorf("workspace_unattributed.cost_usd = %#v, want 5.5", un["cost_usd"])
	}
}

// A store that cannot answer must not fabricate a zero. Zero unattributed spend is a strong,
// checkable claim ("everything reconciles"); an unread store is not entitled to make it.
func TestAICosts_UnattributedReadFailure_OmitsRatherThanClaimsZero(t *testing.T) {
	h := NewHandler(New("", ""), stubUnattributed{
		stubIssues: stubIssues{issue: &model.Issue{ID: "iss-1", WorkspaceID: "ws-A"}},
		err:        errors.New("db down"),
	})

	rr := httptest.NewRecorder()
	h.GetAICosts(rr, wsReq(t, "ws-A"))
	if _, present := decodeBody(t, rr)["unattributed"]; present {
		t.Errorf("unattributed reported despite a read failure — a fabricated $0 reads as "+
			"'fully reconciled': %s", rr.Body.String())
	}

	rr2 := httptest.NewRecorder()
	h.GetIssueAICosts(rr2, issueReq(t, "ws-A"))
	if _, present := decodeBody(t, rr2)["workspace_unattributed"]; present {
		t.Errorf("workspace_unattributed reported despite a read failure: %s", rr2.Body.String())
	}
}

// A reader that does not implement the optional seam (older wiring, tests) must degrade to
// the previous response rather than panic.
func TestAICosts_ReaderWithoutUnattributedSeam_StillServes(t *testing.T) {
	h := NewHandler(New("", ""), stubIssues{issue: &model.Issue{ID: "iss-1", WorkspaceID: "ws-A"}})

	rr := httptest.NewRecorder()
	h.GetIssueAICosts(rr, issueReq(t, "ws-A"))
	if rr.Code != http.StatusOK {
		t.Fatalf("status %d, want 200", rr.Code)
	}
	if _, present := decodeBody(t, rr)["workspace_unattributed"]; present {
		t.Errorf("a reader with no unattributed seam must omit the block, not invent one")
	}
}
