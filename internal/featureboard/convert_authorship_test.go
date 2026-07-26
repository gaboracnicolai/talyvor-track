package featureboard_test

import (
	"encoding/json"
	"net/http"
	"testing"
)

// AUDIT SWEEP (forged authorship, 2nd instance): the audit named issue.Handler.Create.
// Sweeping the class across every write that persists an actor found a second live one
// here — AdminConvert took creator_id from the request body, with a comment rationalising
// it ("The converting admin supplies their own member id"). The authorized member id is
// already in context; asking the client to name itself is the same defect wearing a
// justification. This is exactly how the class survived the first time: it was closed for
// comments and not swept.

// convertedIssueCreator resolves the created issue's persisted creator_id.
func convertedIssueCreator(t *testing.T, body []byte) string {
	t.Helper()
	var out struct {
		IssueID string `json:"issue_id"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decode convert response: %s", body)
	}
	return out.IssueID
}

// RED before the fix: the issue is created signed by whatever string the caller sent.
// GREEN after: signed by the authorized member (convert() injects "admin-1" as the
// resolved actor, mirroring what the authz middleware sets in production).
func TestAdminConvert_ForgedCreator_IsIgnored(t *testing.T) {
	d, r, wsID, boardID, postID, teamID := convertEnv(t)
	rr := convert(t, r, wsID, boardID, postID,
		`{"team_id":"`+teamID+`","creator_id":"someone-else-entirely"}`)

	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%s", rr.Code, rr.Body.String())
	}
	issueID := convertedIssueCreator(t, rr.Body.Bytes())
	got := issueCreatorFromDB(t, d, issueID)
	if got == "someone-else-entirely" {
		t.Fatalf("FORGED AUTHORSHIP: converted issue persisted with creator_id=%q from the request body — "+
			"the actor must come from the authorized membership", got)
	}
	if got != "admin-1" {
		t.Fatalf("creator_id = %q, want %q (the authorized member)", got, "admin-1")
	}
}

// Omitting creator_id must now be the NORMAL case, not a 400: the server already knows
// the caller. The previous contract error was the defect asking to be fed.
func TestAdminConvert_OmittedCreator_ResolvesToCaller(t *testing.T) {
	d, r, wsID, boardID, postID, teamID := convertEnv(t)
	rr := convert(t, r, wsID, boardID, postID, `{"team_id":"`+teamID+`"}`)

	if rr.Code != http.StatusCreated {
		t.Fatalf("convert without creator_id = %d, want 201 (the caller is known server-side); body=%s",
			rr.Code, rr.Body.String())
	}
	if got := issueCreatorFromDB(t, d, convertedIssueCreator(t, rr.Body.Bytes())); got != "admin-1" {
		t.Fatalf("creator_id = %q, want %q", got, "admin-1")
	}
}
