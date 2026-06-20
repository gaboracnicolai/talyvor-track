// Package httpx holds request-shape hardening shared by every handler: a body-size
// cap (so no request body is read unbounded), strict JSON decoding (unknown fields
// rejected, not silently dropped), and Content-Type validation on JSON routes.
package httpx

import (
	"encoding/json"
	"errors"
	"mime"
	"net/http"
	"strings"
)

// Body-size caps. The default covers JSON API bodies; webhooks and the bulk importer
// get explicit, larger — but still bounded — limits.
const (
	DefaultMaxBody       int64 = 1 << 20  // 1 MiB — JSON API + the small Lens webhook
	GitHubWebhookMaxBody int64 = 5 << 20  // 5 MiB — GitHub PR-event payloads
	ImportMaxBody        int64 = 96 << 20 // 96 MiB — multipart CSV upload (importer)
)

// BodyLimit wraps r.Body in http.MaxBytesReader so any later read (json decode or
// io.ReadAll) is bounded; exceeding the cap surfaces as *http.MaxBytesError. The cap
// is chosen by path: the importer and GitHub webhook get larger explicit limits, the
// default otherwise. Applied once as router middleware, it covers every route —
// including ones that read the raw body (the webhooks).
func BodyLimit(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Body != nil && r.Body != http.NoBody {
			limit := DefaultMaxBody
			switch {
			case strings.HasPrefix(r.URL.Path, "/v1/import/"):
				limit = ImportMaxBody
			case r.URL.Path == "/v1/webhooks/github":
				limit = GitHubWebhookMaxBody
			}
			r.Body = http.MaxBytesReader(w, r.Body, limit)
		}
		next.ServeHTTP(w, r)
	})
}

type apiError struct {
	Error string `json:"error"`
	Code  string `json:"code"`
}

func writeError(w http.ResponseWriter, status int, code, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(apiError{Error: msg, Code: code})
}

// DecodeJSON validates the request for a JSON body and strictly decodes it into dst.
// It enforces:
//   - Content-Type is application/json when a Content-Type is present (415 otherwise);
//   - the body is within the cap set by BodyLimit (413 when exceeded);
//   - the JSON is a single object with no unknown or trailing fields (400 otherwise).
//
// On any failure it writes the response itself and returns false — the caller just
// returns. A missing Content-Type is allowed (programmatic clients often omit it; the
// body cap + strict decode still apply).
func DecodeJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	if ct := r.Header.Get("Content-Type"); ct != "" {
		if mt, _, err := mime.ParseMediaType(ct); err != nil || mt != "application/json" {
			writeError(w, http.StatusUnsupportedMediaType, "UNSUPPORTED_MEDIA_TYPE", "Content-Type must be application/json")
			return false
		}
	}
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			writeError(w, http.StatusRequestEntityTooLarge, "BODY_TOO_LARGE", "request body too large")
			return false
		}
		writeError(w, http.StatusBadRequest, "BAD_JSON", err.Error())
		return false
	}
	if dec.More() {
		writeError(w, http.StatusBadRequest, "BAD_JSON", "unexpected trailing data after JSON body")
		return false
	}
	return true
}
