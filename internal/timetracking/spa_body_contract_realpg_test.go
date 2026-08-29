package timetracking_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/talyvor/track/internal/authz"
	"github.com/talyvor/track/internal/testutil"
	"github.com/talyvor/track/internal/timetracking"
)

// spa_body_contract_realpg_test.go — the timer write paths, driven with the field names the
// shipped SPA actually puts in the request body, READ OUT OF THE SPA AT TEST TIME.
//
// ⚠ THE DEFECT THIS FILE WAS WRITTEN RED AGAINST, AND WHY NOTHING ELSE COULD SEE IT.
// httpx.DecodeJSON calls dec.DisallowUnknownFields(), so ANY body field the handler's decode
// struct does not declare is a hard 400 BAD_JSON before a line of handler logic runs. SEC-5
// retired the caller-supplied member id from every timer path — correctly — by DELETING it from
// the decode structs, and the SPA went on sending `member_id` in the body of startTimer and
// logTime. Measured on this branch's first commit, against real Postgres:
//
//	POST /v1/workspaces/{}/timer/start  → 400 {"error":"json: unknown field \"member_id\"","code":"BAD_JSON"}
//	POST /v1/workspaces/{}/time-entries → 400 {"error":"json: unknown field \"member_id\"","code":"BAD_JSON"}
//
// Starting a timer and logging time were both DEAD in the shipped UI. The path was right, the
// method was right and the response type was right, so cmd/track's route census (path+method) and
// field-contract census (response shape) were both green over it, and Go handler tests build their
// own bodies so they never send what the SPA sends. api/websocket.ts made exactly this correction
// for /v1/ws in #44 ("member_id is no longer sent: … the server … derives the member from the
// gateway-verified context"); these call sites were missed.
//
// ⚠⚠ WHY THE BODY IS SCANNED OUT OF frontend/src AND NOT WRITTEN DOWN HERE. The first version of
// this test hard-coded the body the SPA was sending. It went red correctly — and the only way to
// make it green was to EDIT THE LITERAL, at which point it asserts that the handler accepts a
// string this file chose, which was never in doubt. A hand-copied body is a second source of
// truth that agrees with itself. Scanning means re-adding a field to the SPA turns this red.
//
// ⚠ THE SCANNER HAS A FLOOR because it reports what it FOUND, and a scanner that has gone blind
// finds nothing and agrees with everything. If it stops seeing the body literals it fails instead.
//
// ⚠ WHAT THIS FILE DOES NOT CLAIM. It is scoped to the two timer WRITE paths, not to the whole
// SPA→server request-body surface; the general census is a separate instrument (a `body:` literal
// cannot always be attributed to a request literal by scanning, and 7 of the SPA's mutating call
// sites pass a caller-supplied `Partial<T>` that no static read resolves). The claim here is
// narrow and behavioural: every field name the timer client can put on the wire is a field these
// two handlers accept.

func timerReq(t *testing.T, method, path, wsID, memberID, body string) *http.Request {
	t.Helper()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r = r.WithContext(authz.WithAuthorizedRole(r.Context(), wsID, memberID, authz.RoleMember))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("wsID", wsID)
	return r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rctx))
}

func spaFile(t *testing.T, rel string) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("resolve repo root: %v", err)
	}
	b, err := os.ReadFile(filepath.Join(root, rel))
	if err != nil {
		t.Fatalf("read %s: %v — the SPA file this guard reads its bodies from is gone, and a "+
			"guard that cannot find its input must fail, not pass: %v", rel, err, err)
	}
	return string(b)
}

// braceBody returns the object literal that starts at the first '{' at or after idx, brace-matched.
func braceBody(src string, idx int) (string, bool) {
	i := strings.IndexByte(src[idx:], '{')
	if i < 0 {
		return "", false
	}
	i += idx
	depth := 0
	for j := i; j < len(src); j++ {
		switch src[j] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return src[i : j+1], true
			}
		}
	}
	return "", false
}

var (
	reEntryKey  = regexp.MustCompile(`(?m)(?:^|[{,])\s*([A-Za-z_][A-Za-z0-9_]*)\s*[:,}?]`)
	reSpread    = regexp.MustCompile(`\.\.\.([A-Za-z_][A-Za-z0-9_]*)`)
	reShorthand = regexp.MustCompile(`(?m)(?:^|[{,])\s*([A-Za-z_][A-Za-z0-9_]*)\s*(?:,|\s*\})`)
)

// literalKeys returns the json keys of a `{ ... }` object literal or object TYPE, resolving one
// level of `...ident` spread against a `(ident: { ... })` parameter annotation in the same file.
func literalKeys(t *testing.T, src, lit string) []string {
	t.Helper()
	seen := map[string]bool{}
	for _, m := range reEntryKey.FindAllStringSubmatch(lit, -1) {
		seen[m[1]] = true
	}
	for _, m := range reShorthand.FindAllStringSubmatch(lit, -1) {
		seen[m[1]] = true
	}
	for _, m := range reSpread.FindAllStringSubmatch(lit, -1) {
		delete(seen, m[1])
		i := strings.Index(src, "("+m[1]+": {")
		if i < 0 {
			t.Fatalf("the SPA spreads ...%s into a request body and this scanner cannot resolve "+
				"it to a type annotation in the same file. An unresolved spread is an UNKNOWN set "+
				"of body fields, which is the exact shape this guard exists to refuse — resolve it "+
				"or narrow the SPA, do not delete this check.", m[1])
		}
		sub, ok := braceBody(src, i)
		if !ok {
			t.Fatalf("unterminated type annotation for ...%s", m[1])
		}
		for _, k := range literalKeys(t, src, sub) {
			seen[k] = true
		}
	}
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// startTimerBodyKeys reads the body literal of timeApi.startTimer().
func startTimerBodyKeys(t *testing.T) []string {
	t.Helper()
	src := spaFile(t, filepath.Join("frontend", "src", "api", "timetracking.ts"))
	i := strings.Index(src, "startTimer(")
	if i < 0 {
		t.Fatal("timeApi.startTimer is gone from frontend/src/api/timetracking.ts — this guard " +
			"has lost its subject and must fail rather than report a clean timer surface")
	}
	j := strings.Index(src[i:], "body:")
	if j < 0 {
		t.Fatal("startTimer no longer sends a body; if that is intended, this guard needs rewriting")
	}
	lit, ok := braceBody(src, i+j)
	if !ok {
		t.Fatal("unterminated body literal in startTimer")
	}
	return literalKeys(t, src, lit)
}

// logTimeBodyKeys reads the object useLogTime() hands to timeApi.logTime, spread included.
func logTimeBodyKeys(t *testing.T) []string {
	t.Helper()
	src := spaFile(t, filepath.Join("frontend", "src", "hooks", "useTimeTracking.ts"))
	i := strings.Index(src, "timeApi.logTime(")
	if i < 0 {
		t.Fatal("useLogTime no longer calls timeApi.logTime — this guard has lost its subject")
	}
	// the first '{' after the call opens the body object (the first arg is workspaceId)
	lit, ok := braceBody(src, i)
	if !ok {
		t.Fatal("unterminated body literal in useLogTime")
	}
	return literalKeys(t, src, lit)
}

// ⚠ FLOORS. Both scanners report a SET, and a scanner that has gone blind reports the empty set —
// which would make every probe below trivially pass. Measured on this branch: startTimer sends
// {description, issue_id}; useLogTime sends {billable, description, issue_id, started_at,
// stopped_at}. Floored below those, not at them.
const (
	minStartTimerKeys = 2
	minLogTimeKeys    = 4
)

func probe(t *testing.T, name string, rr *httptest.ResponseRecorder) {
	t.Helper()
	if rr.Code == http.StatusBadRequest && strings.Contains(rr.Body.String(), "unknown field") {
		t.Fatalf("%s REJECTS A FIELD THE SHIPPED SPA SENDS: %d %s\n"+
			"httpx.DecodeJSON uses DisallowUnknownFields, so this is a hard 400 before any "+
			"handler logic runs — the feature is dead in the shipped UI while every path, method "+
			"and response-shape census stays green. Either the SPA must stop sending the field or "+
			"the handler must declare it (internal/featureboard AdminConvert keeps `creator_id` "+
			"declared-and-ignored for exactly this reason).", name, rr.Code, rr.Body.String())
	}
}

func TestTimerWritePathsAcceptEveryFieldTheShippedSPASends(t *testing.T) {
	d := testutil.New(t)
	ws := d.Workspace(t)
	tm := d.Team(t, ws.ID)
	iss := d.Issue(t, ws.ID, tm.ID)
	mem := seedMemberID(t, d, ws.ID, "spa-body@x.com")
	h := timetracking.NewHandler(timetracking.NewStore(d.Pool))

	startKeys := startTimerBodyKeys(t)
	logKeys := logTimeBodyKeys(t)
	if len(startKeys) < minStartTimerKeys || len(logKeys) < minLogTimeKeys {
		t.Fatalf("the SPA body scanner went blind: startTimer=%v (want >=%d), useLogTime=%v "+
			"(want >=%d). Do not lower these to make a red go green — find out why the scan "+
			"stopped seeing the SPA's request bodies.",
			startKeys, minStartTimerKeys, logKeys, minLogTimeKeys)
	}
	t.Logf("SPA timer body fields — startTimer=%v useLogTime=%v", startKeys, logKeys)

	// Values for the fields the SPA is known to send. A field the scanner finds that is NOT here
	// is one the SPA has newly started sending: it gets a string, which is enough to prove whether
	// the decoder ACCEPTS THE NAME (a type mismatch is a different 400 and not this test's claim).
	val := map[string]string{
		"issue_id":    `"` + iss.ID + `"`,
		"description": `"work"`,
		"started_at":  `"2026-08-29T09:00:00Z"`,
		"stopped_at":  `"2026-08-29T10:00:00Z"`,
		"billable":    `true`,
	}
	build := func(keys []string) string {
		parts := make([]string, 0, len(keys))
		for _, k := range keys {
			v, ok := val[k]
			if !ok {
				v = `"x"`
			}
			parts = append(parts, `"`+k+`":`+v)
		}
		return "{" + strings.Join(parts, ",") + "}"
	}

	rr := httptest.NewRecorder()
	h.StartTimer(rr, timerReq(t, http.MethodPost, "/v1/workspaces/"+ws.ID+"/timer/start", ws.ID, mem, build(startKeys)))
	probe(t, "POST /v1/workspaces/{wsID}/timer/start", rr)
	if rr.Code != http.StatusCreated {
		t.Fatalf("POST /timer/start with the SPA's own body: want 201, got %d: %s", rr.Code, rr.Body.String())
	}

	rr = httptest.NewRecorder()
	h.LogTime(rr, timerReq(t, http.MethodPost, "/v1/workspaces/"+ws.ID+"/time-entries", ws.ID, mem, build(logKeys)))
	probe(t, "POST /v1/workspaces/{wsID}/time-entries", rr)
	if rr.Code != http.StatusCreated {
		t.Fatalf("POST /time-entries with the SPA's own body: want 201, got %d: %s", rr.Code, rr.Body.String())
	}

	// ⚠ POSITIVE CONTROL, IN THE OTHER DIRECTION. Everything above would also pass if somebody
	// removed DisallowUnknownFields — the probe would stop being able to fire at all. A field the
	// SPA does NOT send must still be refused, on both endpoints. This is also what pins the fix
	// to the CLIENT: repairing the defect by widening the server's request contract turns this red.
	for _, tc := range []struct {
		name string
		call func(http.ResponseWriter, *http.Request)
		path string
		body string
	}{
		{"timer/start", h.StartTimer, "/timer/start", `{"issue_id":"` + iss.ID + `","description":"c","not_a_real_field":1}`},
		{"time-entries", h.LogTime, "/time-entries", `{"issue_id":"` + iss.ID + `","started_at":"2026-08-29T09:00:00Z","not_a_real_field":1}`},
	} {
		rr := httptest.NewRecorder()
		tc.call(rr, timerReq(t, http.MethodPost, "/v1/workspaces/"+ws.ID+tc.path, ws.ID, mem, tc.body))
		if rr.Code != http.StatusBadRequest || !strings.Contains(rr.Body.String(), "unknown field") {
			t.Fatalf("CONTROL FAILED on %s — an unknown body field was NOT refused (got %d: %s). "+
				"DisallowUnknownFields is the mechanism this whole guard measures against; if it "+
				"is gone the probes above cannot fire and their green means nothing.",
				tc.name, rr.Code, rr.Body.String())
		}
	}
}
