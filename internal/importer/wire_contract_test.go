package importer

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// wire_contract_test.go — THE ENDPOINT ITSELF, PINNED AT THE WIRE.
//
// #74 pinned the `fields` LIST at the wire (TestJiraRequest_AsksForTheDateFields) because narrowing
// it would silently take a field away while every fixture kept passing. The ENDPOINT had the same
// hole and nobody had closed it: before this file, `jiraSearchPath` and the string "search/jql"
// appeared in ZERO test files, and every fake in this package is an http.HandlerFunc that answers
// ANY path with the same body. Point the client at /rest/api/2/search, at /v3/search, at "" — the
// whole suite stays green.
//
// ⚠ THAT IS NOT A HYPOTHETICAL RISK, AND jira.go SAYS SO ITSELF: its own header records that "the old
// /search is 410 Gone". This endpoint has ALREADY moved once. The next move is silent here.
//
// ⚠ THE ASSERTION HARDCODES THE LITERAL ON PURPOSE. Writing `if r.URL.Path != jiraSearchPath` is the
// obvious form and it is VACUOUS — it compares the constant to itself and passes for every possible
// value, including "". A guard that cannot fail is not a guard. The literal below is the second
// copy that makes an edit to the constant a deliberate two-place change instead of a silent one.
//
// ⚠ WHAT THIS FILE DOES NOT CLAIM, because it cannot be measured from here. The shipped client calls
// Jira CLOUD (REST v3, POST /rest/api/3/search/jql, description as ADF). Every "measured against a
// real Jira" note in this package — #73's statusCategory vocabulary, #74's date layouts — was taken
// against jira.atlassian.com on /rest/api/2/, which is a Jira SERVER/DC instance and does not serve
// v3 at all: /rest/api/3/search/jql there answers 302 to Atlassian SSO (measured 2026-08-09,
// negative-controlled first — a fabricated host resolved to nothing and a fabricated path on the
// real host answered 404, so the 200s were not blanket). Those shapes are stable across v2 and v3
// and the conclusions are almost certainly right — but this package already contains one field where
// v2 and v3 DISAGREE (description: plain text on v2, ADF JSON on v3, which jira.go handles), so
// "measured on v2" is weaker evidence for v3 than it reads. Proving the v3 shapes needs a real Cloud
// tenant, which is item (3) on W3.4 and is not available in this environment.

const measuredJiraCloudSearchPath = "/rest/api/3/search/jql"

// TestJiraRequest_PinsTheEndpointAndMethod asserts what the SERVER actually receives, driven through
// the real jiraSource rather than by reading the constant.
func TestJiraRequest_PinsTheEndpointAndMethod(t *testing.T) {
	var (
		gotPath   string
		gotMethod string
		gotAuth   string
		hits      int
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		gotPath, gotMethod, gotAuth = r.URL.Path, r.Method, r.Header.Get("Authorization")
		_, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"issues":[],"isLast":true}`))
	}))
	defer srv.Close()

	src := newJiraSource("e:t", "PROJ", srv.URL, srv.Client())
	src.client.retry.sleep = noopSleep
	src.Next()

	// NON-VACUITY: if the client never called out, every assertion below would compare "" to "" for
	// the wrong reason. The date-fields wire test has the same dependency and does not state it.
	if hits != 1 {
		t.Fatalf("expected exactly 1 outgoing request, got %d — the assertions below would be vacuous", hits)
	}
	if gotPath != measuredJiraCloudSearchPath {
		t.Errorf("outgoing path = %q, want %q\n"+
			"Jira's old /search is 410 Gone; a wrong path fails against every real tenant while this "+
			"package's fakes answer any path.", gotPath, measuredJiraCloudSearchPath)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("outgoing method = %q, want POST — /rest/api/3/search/jql is a POST endpoint", gotMethod)
	}
	if !strings.HasPrefix(gotAuth, "Basic ") {
		t.Errorf("Authorization = %q, want a Basic credential — jira.go's contract is Basic email:token", gotAuth)
	}
}

// TestJiraSearchPath_ConstantMatchesTheMeasuredEndpoint is the second copy referred to above. It is
// deliberately a SEPARATE assertion from the wire test: the wire test proves the request goes where
// the constant says, and this proves the constant says what a real Jira Cloud actually serves.
func TestJiraSearchPath_ConstantMatchesTheMeasuredEndpoint(t *testing.T) {
	if jiraSearchPath != measuredJiraCloudSearchPath {
		t.Errorf("jiraSearchPath = %q, want %q — if Jira moved the endpoint again, change BOTH and say "+
			"where the new one was measured.", jiraSearchPath, measuredJiraCloudSearchPath)
	}
	if !strings.HasPrefix(jiraSearchPath, "/") {
		t.Errorf("jiraSearchPath = %q must start with '/' — newJiraClient concatenates it onto a "+
			"TrimRight'd base URL, so a missing slash silently produces hostsearch/jql", jiraSearchPath)
	}
}
