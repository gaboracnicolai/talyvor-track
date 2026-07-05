package member_test

import (
	"bytes"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/talyvor/track/internal/testutil"
)

// captureLogs swaps slog.Default for a JSON logger writing to a buffer, restored on
// cleanup. Member tests run sequentially in-package, so the global swap is safe.
func captureLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	orig := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(orig) })
	return &buf
}

// (HARDEN 1 + adversarial PII sweep) a successful pull emits ONE audit line carrying the
// workspace_id + row COUNT + status — and NEVER a member email (a log that copies the
// roster is a second leak).
func TestServiceMembers_AuditLog_CountNotRoster(t *testing.T) {
	d := testutil.New(t)
	ws := d.Workspace(t)
	seedMember(t, d, ws.ID, "alice@corp.com", "admin")
	seedMember(t, d, ws.ID, "carol@corp.com", "member")
	logs := captureLogs(t)

	rr := httptest.NewRecorder()
	memberChain(testSyncSecret, d).ServeHTTP(rr, getReq(ws.ID, testSyncSecret))
	if rr.Code != 200 {
		t.Fatalf("pull = %d, want 200", rr.Code)
	}
	out := logs.String()
	if !strings.Contains(out, "service_member_pull") {
		t.Fatalf("no audit line emitted: %s", out)
	}
	if !strings.Contains(out, ws.ID) || !strings.Contains(out, `"count":2`) {
		t.Fatalf("audit line missing workspace_id or count=2: %s", out)
	}
	if strings.Contains(out, "alice@corp.com") || strings.Contains(out, "carol@corp.com") {
		t.Fatalf("AUDIT LOG LEAKED PII — member email in the log line: %s", out)
	}
}

// (HARDEN 2) a pull for a NON-EXISTENT workspace_id → empty array (200), and is audited
// the same way (count=0) so sequential-ID probing by a leaked token lights up the log.
func TestServiceMembers_NonExistentWorkspace_EmptyAndAudited(t *testing.T) {
	d := testutil.New(t)
	// seed a member in a DIFFERENT real workspace so the DB isn't trivially empty.
	seedMember(t, d, d.Workspace(t).ID, "alice@corp.com", "admin")
	logs := captureLogs(t)

	rr := httptest.NewRecorder()
	memberChain(testSyncSecret, d).ServeHTTP(rr, getReq("ws-does-not-exist-999", testSyncSecret))
	if rr.Code != 200 {
		t.Fatalf("non-existent workspace = %d, want 200 (empty array)", rr.Code)
	}
	if got := strings.TrimSpace(rr.Body.String()); got != "[]" {
		t.Fatalf("non-existent workspace body = %q, want []", got)
	}
	out := logs.String()
	if !strings.Contains(out, "service_member_pull") ||
		!strings.Contains(out, "ws-does-not-exist-999") ||
		!strings.Contains(out, `"count":0`) {
		t.Fatalf("probe not audited (event/workspace_id/count=0 expected): %s", out)
	}
	if strings.Contains(out, "alice@corp.com") {
		t.Fatalf("AUDIT LOG LEAKED PII on a probe: %s", out)
	}
}
