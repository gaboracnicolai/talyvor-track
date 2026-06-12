package notification

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
)

type fakeLister struct{ rows []DeadLetter }

func (f fakeLister) List(_ context.Context, _ int) ([]DeadLetter, error) { return f.rows, nil }

func TestHandler_ListDeadLetters(t *testing.T) {
	h := NewHandler(nil).WithDeadLetters(fakeLister{rows: []DeadLetter{
		{ID: 1, Subject: "ENG-1 assigned", Attempts: 3, LastError: "smtp down", Recipients: []string{"a@b.c"}},
	}})
	r := chi.NewRouter()
	h.Mount(r)

	req := httptest.NewRequest("GET", "/workspaces/ws1/notifications/dead-letters", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	var out []DeadLetter
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out) != 1 || out[0].Subject != "ENG-1 assigned" {
		t.Fatalf("unexpected body: %s", w.Body.String())
	}
}

func TestHandler_ListDeadLetters_NotConfiguredReturnsEmpty(t *testing.T) {
	// Email/dead-letter not wired: the surface returns an empty list (200),
	// not an error — nothing has failed because nothing sends.
	h := NewHandler(nil)
	r := chi.NewRouter()
	h.Mount(r)

	req := httptest.NewRequest("GET", "/workspaces/ws1/notifications/dead-letters", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	if got := w.Body.String(); got != "[]\n" && got != "[]" {
		t.Fatalf("want empty JSON array, got %q", got)
	}
}
