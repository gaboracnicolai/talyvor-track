package importer

import (
	"bytes"
	"context"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/talyvor/track/internal/authz"
	"github.com/talyvor/track/internal/testutil"
)

// An upload one byte over the cap is refused with 413 TOO_LARGE and writes no job; an upload exactly
// at the cap is accepted. The cap is injected small so the branch is cheap to reach (B18.34); the
// production value is asserted separately so the injection cannot hide a changed bound.
func TestJobHandler_AnUploadOverTheCapIsRefused(t *testing.T) {
	if got := NewJobHandler(nil).maxUpload; got != 64<<20 {
		t.Fatalf("production upload cap = %d bytes, want 64 MiB (%d)", got, 64<<20)
	}

	d := testutil.New(t)
	ws := d.Workspace(t)
	team := d.Team(t, ws.ID)
	h := NewJobHandler(NewJobStore(d.Pool))
	h.maxUpload = 1024

	upload := func(size int) *httptest.ResponseRecorder {
		var body bytes.Buffer
		mw := multipart.NewWriter(&body)
		fw, err := mw.CreateFormFile("file", "export.csv")
		if err != nil {
			t.Fatal(err)
		}
		_, _ = fw.Write(bytes.Repeat([]byte("a"), size))
		_ = mw.Close()
		req := httptest.NewRequest(http.MethodPost,
			"/v1/import/jobs?workspace_id="+ws.ID+"&team_id="+team.ID+"&source_type=linear_csv", &body)
		req.Header.Set("Content-Type", mw.FormDataContentType())
		req = req.WithContext(authz.WithMemberships(req.Context(),
			[]authz.Membership{{WorkspaceID: ws.ID, MemberID: "m1", Role: authz.RoleOwner}}))
		rr := httptest.NewRecorder()
		h.create(rr, req)
		return rr
	}
	jobs := func() int {
		var n int
		if err := d.Pool.QueryRow(context.Background(),
			`SELECT count(*) FROM import_jobs WHERE workspace_id=$1`, ws.ID).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	rr := upload(1025)
	if rr.Code != http.StatusRequestEntityTooLarge || !strings.Contains(rr.Body.String(), "TOO_LARGE") {
		t.Fatalf("upload of cap+1 bytes = %d %s, want 413 TOO_LARGE", rr.Code, rr.Body.String())
	}
	if n := jobs(); n != 0 {
		t.Fatalf("a refused upload wrote %d job row(s), want 0", n)
	}

	if rr := upload(1024); rr.Code != http.StatusAccepted {
		t.Fatalf("upload of exactly the cap = %d %s, want 202", rr.Code, rr.Body.String())
	}
}
