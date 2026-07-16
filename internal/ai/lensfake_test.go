package ai

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// sharedAdminKey is the ONE key Track holds. After the fix it may ride
// ONLY the mint endpoint — never a data-path call. The fake asserts that.
const sharedAdminKey = "tlv_SHARED_admin_key"

// dataCall records one proxy (data-path) request the fake Lens received.
type dataCall struct {
	path    string
	bearer  string
	feature string // X-Talyvor-Feature
}

// fakeLens is an httptest Lens that serves the admin-gated mint endpoint
// AND the two LLM proxy data paths. It records every mint (with the key
// used) and every data-path bearer, so a test can prove tenant isolation
// end-to-end: each workspace's calls carry that workspace's minted JWT,
// the admin key is spent only on minting, and it is cached per workspace.
type fakeLens struct {
	server *httptest.Server

	mu             sync.Mutex
	mints          int
	mintByWs       map[string]int
	mintAuth       []string
	dataCalls      []dataCall
	anthropicReply string // body returned by the anthropic proxy
}

func newAIFakeLens(t *testing.T) *fakeLens {
	t.Helper()
	f := &fakeLens{
		mintByWs:       map[string]int{},
		anthropicReply: anthropicResp(`{"suggested_priority":2,"suggested_labels":["bug"],"summary":"x","confidence":0.8}`),
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/auth/token", f.handleMint)
	mux.HandleFunc("/v1/proxy/anthropic/v1/messages", f.handleAnthropic)
	mux.HandleFunc("/v1/proxy/openai/v1/embeddings", f.handleEmbeddings)
	f.server = httptest.NewServer(mux)
	t.Cleanup(f.server.Close)
	return f
}

func (f *fakeLens) handleMint(w http.ResponseWriter, r *http.Request) {
	var body struct {
		WorkspaceID string `json:"workspace_id"`
		TTLHours    int    `json:"ttl_hours"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	f.mu.Lock()
	f.mints++
	f.mintByWs[body.WorkspaceID]++
	f.mintAuth = append(f.mintAuth, r.Header.Get("Authorization"))
	ordinal := f.mintByWs[body.WorkspaceID]
	f.mu.Unlock()

	// The issued token ENCODES its workspace so a data-path handler can
	// recover the claim and prove attribution didn't collapse.
	tok := "jwt." + body.WorkspaceID + "." + itoa(ordinal)
	exp := time.Now().Add(time.Duration(body.TTLHours) * time.Hour)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"token":      tok,
		"expires_at": exp.Format(time.RFC3339Nano),
	})
}

func (f *fakeLens) handleAnthropic(w http.ResponseWriter, r *http.Request) {
	f.record(r)
	_, _ = w.Write([]byte(f.anthropicReply))
}

func (f *fakeLens) handleEmbeddings(w http.ResponseWriter, r *http.Request) {
	f.record(r)
	_, _ = w.Write([]byte(`{"data":[{"embedding":[0.1,0.2,0.3]}]}`))
}

func (f *fakeLens) record(r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.dataCalls = append(f.dataCalls, dataCall{
		path:    r.URL.Path,
		bearer:  strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "),
		feature: r.Header.Get("X-Talyvor-Feature"),
	})
}

func (f *fakeLens) snapshot() (mints int, mintByWs map[string]int, mintAuth []string, calls []dataCall) {
	f.mu.Lock()
	defer f.mu.Unlock()
	mintByWs = map[string]int{}
	for k, v := range f.mintByWs {
		mintByWs[k] = v
	}
	return f.mints, mintByWs, append([]string(nil), f.mintAuth...), append([]dataCall(nil), f.dataCalls...)
}

// workspaceOf recovers the workspace a fake-minted "jwt.<ws>.<n>" token
// claims. Returns ("", false) for anything that isn't such a token —
// e.g. the raw shared key, which has no "jwt." prefix.
func workspaceOf(bearer string) (string, bool) {
	if !strings.HasPrefix(bearer, "jwt.") {
		return "", false
	}
	parts := strings.Split(bearer, ".")
	if len(parts) != 3 {
		return "", false
	}
	return parts[1], true
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
