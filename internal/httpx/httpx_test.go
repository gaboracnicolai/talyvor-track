package httpx_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/talyvor/track/internal/httpx"
)

type payload struct {
	Name string `json:"name"`
}

// server wires BodyLimit → a handler that strictly decodes a payload and 200s.
func server() http.Handler {
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var p payload
		if !httpx.DecodeJSON(w, r, &p) {
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	})
	return httpx.BodyLimit(h)
}

func do(t *testing.T, path, contentType, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	rr := httptest.NewRecorder()
	server().ServeHTTP(rr, req)
	return rr
}

func TestDecodeJSON_ValidRequestSucceeds(t *testing.T) {
	if rr := do(t, "/v1/issues", "application/json", `{"name":"ok"}`); rr.Code != http.StatusOK {
		t.Fatalf("valid request = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
}

func TestDecodeJSON_UnknownFieldRejected(t *testing.T) {
	if rr := do(t, "/v1/issues", "application/json", `{"name":"ok","sneaky":1}`); rr.Code != http.StatusBadRequest {
		t.Fatalf("unknown field = %d, want 400; body=%s", rr.Code, rr.Body.String())
	}
}

func TestDecodeJSON_WrongContentTypeRejected(t *testing.T) {
	if rr := do(t, "/v1/issues", "text/plain", `{"name":"ok"}`); rr.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("wrong content-type = %d, want 415", rr.Code)
	}
}

func TestDecodeJSON_TrailingDataRejected(t *testing.T) {
	if rr := do(t, "/v1/issues", "application/json", `{"name":"ok"}{"name":"again"}`); rr.Code != http.StatusBadRequest {
		t.Fatalf("trailing data = %d, want 400", rr.Code)
	}
}

func TestDecodeJSON_OversizedBodyRejected(t *testing.T) {
	// 2 MiB > DefaultMaxBody (1 MiB): the cap must TRIGGER (413), not OOM/hang.
	big := `{"name":"` + strings.Repeat("a", 2<<20) + `"}`
	if rr := do(t, "/v1/issues", "application/json", big); rr.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized body = %d, want 413 (cap must trigger); len=%d", rr.Code, len(big))
	}
}

func TestDecodeJSON_BodyJustUnderCapSucceeds(t *testing.T) {
	// Just under 1 MiB → accepted: proves the cap doesn't reject normal bodies.
	ok := `{"name":"` + strings.Repeat("a", (1<<20)-100) + `"}`
	if rr := do(t, "/v1/issues", "application/json", ok); rr.Code != http.StatusOK {
		t.Fatalf("under-cap body = %d, want 200; len=%d", rr.Code, len(ok))
	}
}

func TestBodyLimit_ImportRouteHasHigherCap(t *testing.T) {
	// A 2 MiB body on an import route is under its 96 MiB cap → must NOT be 413'd,
	// while the same body on a default route IS (proves the per-path cap).
	big := `{"name":"` + strings.Repeat("a", 2<<20) + `"}`
	if rr := do(t, "/v1/import/linear", "application/json", big); rr.Code == http.StatusRequestEntityTooLarge {
		t.Fatalf("import route wrongly 413'd a 2 MiB body (96 MiB cap expected)")
	}
	if rr := do(t, "/v1/issues", "application/json", big); rr.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("default route should 413 a 2 MiB body, got %d", rr.Code)
	}
}

func TestDecodeJSON_MissingContentTypeAllowed(t *testing.T) {
	// Absent Content-Type is allowed (the cap + strict decode still apply).
	if rr := do(t, "/v1/issues", "", `{"name":"ok"}`); rr.Code != http.StatusOK {
		t.Fatalf("missing content-type with valid JSON = %d, want 200", rr.Code)
	}
}
