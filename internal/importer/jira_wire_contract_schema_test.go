package importer

// jira_wire_contract_schema_test.go — WHAT THE SHIPPED JIRA CLIENT PUTS ON THE WIRE, AND WHAT IT
// READS BACK, AGAINST THE CONTRACT ATLASSIAN PUBLISHES.
//
// This is the Jira half of linear_query_schema_test.go, and it exists for the same reason: every
// Jira test in this package answers from a fake server that accepts whatever it is asked, so a
// request key Jira does not accept, or a response field Jira never sends, is invisible to the whole
// suite. wire_contract_test.go pins this transport against a HAND-WRITTEN expectation — a second
// copy of the same assumption — and the thing it cannot do is disagree with Atlassian.
//
// ⚠ WHAT IS MEASURED, AND WHAT IS STILL NOT. testdata/jira_search_contract.json is the PUBLISHED
// v3 spec (openapi 3.0.1, spec version in the file), fetched by scripts/w34-jira-contract-snapshot.py.
// It is a fact about the contract Atlassian commits to. It is NOT a live tenant: no Jira credentials
// exist in this environment and W3.4's open question (3) — whether any `*_api` import has ever run
// end to end against a real instance — is untouched by this file and is not implied to be closed.
//
// ⚠ THE REQUEST HALF DRIVES THE SHIPPED CLIENT AND READS THE BYTES IT SENT, rather than restating
// the map literal in fetchPage. A test that re-declares the request body is a copy of the code, and
// a copy agrees with the code by construction.
//
// ⚠ THE FINDING THIS FILE CARRIES, MEASURED AND NAMED RATHER THAN QUIETLY FIXED:
// `jiraResp.ErrorMessages` reads `errorMessages` off a **200** response, and the 200 schema
// (SearchAndReconcileResults) does not declare that field — `errorMessages` belongs to
// ErrorCollection, the shape Jira returns with a 4xx, which fetchPage already handles one branch
// earlier through firstJiraError. Against a spec-conformant Jira the 200-arm at jira.go:171 cannot
// fire. It is DEFENSIVE and costs nothing, so removing it would trade a harmless arm for a
// behaviour change no measurement here justifies; jiraRespFieldsNotInTheSuccessSchema below names
// it as the single exemption so the NEXT undeclared field is a failure rather than a habit.
//
// ⚠ AND THE MIRROR, WHICH IS THE PART THAT MAY MATTER MORE: the success schema declares `warnings`
// (an array of SearchWarning) and this transport never reads it. Whether a real Jira ever populates
// it, and on what, is NOT measurable from here — so this file REPORTS the gap in
// TestJiraSearchResponse_TheFieldsTheTransportIgnoresAreTheOnesItMeantTo and does not invent a
// handler for a field nobody here has seen filled. Reading a provider's own warnings and dropping
// them is the shape this package has already paid for twice (viaADFNodeDropped, refused rows), and
// it is written into the queue with that history rather than guessed at in code.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

type jiraContractSnapshot struct {
	Provenance struct {
		Source      string `json:"source"`
		FetchedUTC  string `json:"fetched_utc"`
		SpecVersion string `json:"spec_version"`
	} `json:"_provenance"`
	Endpoint struct {
		Path            string   `json:"path"`
		Methods         []string `json:"methods"`
		PostOperationID string   `json:"post_operation_id"`
		PostDeprecated  bool     `json:"post_deprecated"`
		RequestSchema   string   `json:"request_schema"`
		ResponseSchema  string   `json:"response_schema"`
	} `json:"endpoint"`
	OldEndpoint struct {
		Path           string `json:"path"`
		Present        bool   `json:"present"`
		PostDeprecated bool   `json:"post_deprecated"`
		GetDeprecated  bool   `json:"get_deprecated"`
	} `json:"old_endpoint"`
	RequestProperties  map[string]string `json:"request_properties"`
	ResponseProperties map[string]string `json:"response_properties"`
	ErrorCollection    map[string]string `json:"error_collection_properties"`
}

func loadJiraContract(t *testing.T) jiraContractSnapshot {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "jira_search_contract.json"))
	if err != nil {
		t.Fatalf("read the pinned Jira contract: %v\nRefresh it with "+
			"`python3 scripts/w34-jira-contract-snapshot.py`.", err)
	}
	var s jiraContractSnapshot
	if err := json.Unmarshal(raw, &s); err != nil {
		t.Fatalf("parse the pinned Jira contract: %v", err)
	}
	// Anti-vacuity, asserted BEFORE the snapshot is used for anything: an empty property map would
	// make every membership check below pass by having nothing to disagree with.
	if len(s.RequestProperties) < 5 || len(s.ResponseProperties) < 4 {
		t.Fatalf("the pinned contract carries %d request / %d response properties — too few to be the "+
			"published schema. Refresh it; a snapshot with nothing in it is a guard with nothing in it.",
			len(s.RequestProperties), len(s.ResponseProperties))
	}
	return s
}

// captureJiraRequests drives the SHIPPED jiraSource through two pages and returns the decoded bodies
// it POSTed. Two pages, because the second is the only one that carries nextPageToken.
func captureJiraRequests(t *testing.T) []map[string]any {
	t.Helper()
	var bodies []map[string]any
	pages := []string{
		`{"issues":[],"isLast":false,"nextPageToken":"tok2"}`,
		`{"issues":[],"isLast":true}`,
	}
	n := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("the shipped client sent a body that is not JSON: %v", err)
		}
		bodies = append(bodies, body)
		page := pages[len(pages)-1]
		if n < len(pages) {
			page = pages[n]
		}
		n++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(page))
	}))
	defer srv.Close()

	src := newJiraSource(context.Background(), "me@corp.com:api-token", "PROJ", srv.URL, srv.Client())
	src.client.retry.sleep = noopSleep
	if _, _, hitCap := drainSource(t, src, 8); hitCap {
		t.Fatalf("the source never terminated while capturing its requests")
	}
	if len(bodies) < 2 {
		t.Fatalf("captured %d request bodies, want at least 2 — the second page is the only one that "+
			"carries nextPageToken, and a capture that never saw it cannot check it", len(bodies))
	}
	return bodies
}

// TestJiraSearchRequest_EveryKeyItSendsIsDeclaredByAtlassiansSpec — a key Atlassian does not declare
// is not a warning from Jira, it is a 400 on every page of every import.
func TestJiraSearchRequest_EveryKeyItSendsIsDeclaredByAtlassiansSpec(t *testing.T) {
	spec := loadJiraContract(t)
	seen := map[string]bool{}
	for i, body := range captureJiraRequests(t) {
		for key := range body {
			seen[key] = true
			if _, ok := spec.RequestProperties[key]; !ok {
				t.Errorf("request %d sends %q, which SearchAndReconcileRequestBean does not declare "+
					"(declared: %v). Atlassian rejects an unknown body key with a 400, and no test in "+
					"this package can see it — every Jira fake here answers whatever it is asked.",
					i+1, key, sortedKeys(spec.RequestProperties))
			}
		}
	}
	// The floor: a capture that saw nothing would pass the loop above trivially.
	for _, must := range []string{"jql", "fields", "maxResults", "nextPageToken"} {
		if !seen[must] {
			t.Errorf("the shipped client never sent %q across two pages. Either the request stopped "+
				"carrying it — `jql` and `fields` are what make the search a search, and nextPageToken "+
				"is the pagination terminator — or this capture is reading the wrong thing.", must)
		}
	}
}

// jiraRespFieldsNotInTheSuccessSchema is the ONE exemption, and it is a finding rather than an
// allowance: see this file's header. Adding to this list is how a real defect would get normalised,
// so the list is asserted to be exactly this — a NEW undeclared field fails even if it is added here
// without the count below being updated.
var jiraRespFieldsNotInTheSuccessSchema = map[string]string{
	"errorMessages": "belongs to ErrorCollection (the 4xx shape), not to SearchAndReconcileResults; " +
		"the 200-arm at jira.go:171 cannot fire against a spec-conformant Jira",
}

// TestJiraSearchResponse_EveryFieldItReadsIsDeclaredByAtlassiansSpec reflects over the SHIPPED
// jiraResp type, so it reads the struct the transport actually decodes into.
func TestJiraSearchResponse_EveryFieldItReadsIsDeclaredByAtlassiansSpec(t *testing.T) {
	spec := loadJiraContract(t)
	read := jsonTagsOf(t, reflect.TypeOf(jiraResp{}))
	if len(read) < 3 {
		t.Fatalf("reflected %d json fields off jiraResp, want at least 3 — a reflection that finds "+
			"nothing agrees with every schema", len(read))
	}
	for _, tag := range read {
		if _, declared := spec.ResponseProperties[tag]; declared {
			continue
		}
		why, exempt := jiraRespFieldsNotInTheSuccessSchema[tag]
		if !exempt {
			t.Errorf("jiraResp reads %q off a 200, and %s does not declare it. A field the contract "+
				"does not carry decodes to its zero value on every real response — which is exactly "+
				"how a branch that guards nothing looks from inside the suite.",
				tag, spec.Endpoint.ResponseSchema)
			continue
		}
		t.Logf("known exemption: %q — %s", tag, why)
	}
	// The ErrorCollection half of the finding: the exempted field must at least be real SOMEWHERE in
	// the contract. If Atlassian dropped it from the error shape too, the arm is dead in both
	// directions and that is a different sentence.
	if _, ok := spec.ErrorCollection["errorMessages"]; !ok {
		t.Errorf("ErrorCollection no longer declares errorMessages, so firstJiraError() — which is how " +
			"every 4xx message reaches an operator — is reading a field that does not exist")
	}
}

// TestJiraSearchResponse_TheFieldsTheTransportIgnoresAreTheOnesItMeantTo is a CENSUS with an
// explicit list, not an assertion that ignoring is fine. Its job is to make the NEXT field Atlassian
// adds to this response show up as a failure with a sentence, instead of being silently unread the
// way `warnings` has been.
func TestJiraSearchResponse_TheFieldsTheTransportIgnoresAreTheOnesItMeantTo(t *testing.T) {
	spec := loadJiraContract(t)
	read := map[string]bool{}
	for _, tag := range jsonTagsOf(t, reflect.TypeOf(jiraResp{})) {
		read[tag] = true
	}
	var ignored []string
	for name := range spec.ResponseProperties {
		if !read[name] {
			ignored = append(ignored, name)
		}
	}
	sort.Strings(ignored)

	// MEASURED at this merge against spec version 1001.0.0-SNAPSHOT-cf3ba5f…:
	//   names   — the field-id → display-name map, only meaningful with `expand=names`, never requested.
	//   schema  — the field-id → type map, same condition, never requested.
	//   warnings — ⚠ SearchWarning[]. THE PROVIDER'S OWN WARNINGS ABOUT THIS SEARCH, DROPPED. This
	//              transport has a warnings channel all the way to the job row (FieldNote → summarise
	//              → import_jobs.warnings) and does not put Jira's into it. Whether a real instance
	//              ever populates it is NOT measurable without a tenant, so this file names the gap
	//              and refuses to invent a handler for a shape nobody here has seen filled.
	want := []string{"names", "schema", "warnings"}
	if !reflect.DeepEqual(ignored, want) {
		t.Errorf("the response fields this transport ignores are %v, and were %v when that list was "+
			"last measured.\nA field that appeared here is one Atlassian added to a response Track "+
			"already reads and nobody decided about; a field that left is one this comment now "+
			"describes wrongly. Either way, read the header of this file before editing the list.",
			ignored, want)
	}
}

// TestJiraSearchEndpoint_IsTheOneAtlassianStillDeclares pins the endpoint the client POSTs to
// against the spec, including the claim jira.go's own header makes about the OLD one.
func TestJiraSearchEndpoint_IsTheOneAtlassianStillDeclares(t *testing.T) {
	spec := loadJiraContract(t)
	if jiraSearchPath != spec.Endpoint.Path {
		t.Errorf("jiraSearchPath = %q, the published spec declares %q", jiraSearchPath, spec.Endpoint.Path)
	}
	if spec.Endpoint.PostDeprecated {
		t.Errorf("Atlassian now marks POST %s DEPRECATED (operationId %q). Every linear_api sibling "+
			"of this transport got a year of warning from a line like this one; Jira's turn is now.",
			spec.Endpoint.Path, spec.Endpoint.PostOperationID)
	}
	if !hasMethod(spec.Endpoint.Methods, "post") {
		t.Errorf("the spec no longer declares POST on %s — the shipped client only speaks POST",
			spec.Endpoint.Path)
	}
	// jira.go's header says "the old /search is 410 Gone". The spec cannot see a runtime 410; what it
	// CAN say is that both methods on the old path are deprecated and marked as being removed. If
	// that ever flips back, the comment is the thing to re-read.
	if spec.OldEndpoint.Present && (!spec.OldEndpoint.PostDeprecated || !spec.OldEndpoint.GetDeprecated) {
		t.Errorf("%s is declared and NOT deprecated on both methods (post=%v get=%v), which contradicts "+
			"jira.go's header — it says the old endpoint is gone, and that sentence is why this client "+
			"POSTs to %s at all", spec.OldEndpoint.Path, spec.OldEndpoint.PostDeprecated,
			spec.OldEndpoint.GetDeprecated, spec.Endpoint.Path)
	}
}

// ─── helpers ──────────────────────────────────────────────────────────────────────────────────

func jsonTagsOf(t *testing.T, typ reflect.Type) []string {
	t.Helper()
	if typ.Kind() != reflect.Struct {
		t.Fatalf("jsonTagsOf: %s is not a struct", typ)
	}
	var out []string
	for i := 0; i < typ.NumField(); i++ {
		tag := typ.Field(i).Tag.Get("json")
		if tag == "" || tag == "-" {
			continue
		}
		out = append(out, strings.Split(tag, ",")[0])
	}
	sort.Strings(out)
	return out
}

func hasMethod(methods []string, want string) bool {
	for _, m := range methods {
		if m == want {
			return true
		}
	}
	return false
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
