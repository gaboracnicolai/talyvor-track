package cycle

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/pashagolub/pgxmock/v4"

	"github.com/talyvor/track/internal/model"
)

type fakeEmailer struct {
	ended      []model.Cycle
	endedActor []string
}

func (f *fakeEmailer) SprintStarted(_ context.Context, _ model.Cycle, _ string) {}
func (f *fakeEmailer) SprintEnded(_ context.Context, c model.Cycle, actor string) {
	f.ended = append(f.ended, c)
	f.endedActor = append(f.endedActor, actor)
}

func reqWithID(method, id, body string) *http.Request {
	r := httptest.NewRequest(method, "/", strings.NewReader(body))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", id)
	return r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rctx))
}

// The handler must load the cycle, complete it (DB writes), THEN fire the
// SprintEnded email hook — proving the notification is enqueued after the
// store mutation, not inside it.
func TestHandler_CompleteEmitsSprintEndedAfterCommit(t *testing.T) {
	store, pool := newMockStore(t)
	fe := &fakeEmailer{}
	h := NewHandler(store).WithEmailer(fe)

	now := time.Now().UTC()
	pool.ExpectQuery(`SELECT .* FROM cycles WHERE id = \$1`).
		WithArgs("c1").
		WillReturnRows(cycleRow("c1", 1, "active", now, now.Add(time.Hour)))
	pool.ExpectExec(`UPDATE cycles SET status = 'completed'`).
		WithArgs("c1").
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	pool.ExpectExec(`UPDATE issues SET cycle_id = NULL`).
		WithArgs("c1").
		WillReturnResult(pgxmock.NewResult("UPDATE", 2))

	req := reqWithID(http.MethodPost, "c1", "")
	req.Header.Set("X-Member-Id", "actor-1")
	w := httptest.NewRecorder()
	h.Complete(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	if len(fe.ended) != 1 || fe.ended[0].ID != "c1" {
		t.Fatalf("SprintEnded should fire once for c1, got %+v", fe.ended)
	}
	if fe.endedActor[0] != "actor-1" {
		t.Errorf("actor = %q, want actor-1", fe.endedActor[0])
	}
	if err := pool.ExpectationsWereMet(); err != nil {
		t.Errorf("db expectations: %v", err)
	}
}

// With no emailer wired (email disabled), Complete must not touch the cycle
// loader and behaves exactly as before.
func TestHandler_CompleteWithoutEmailerIsUnchanged(t *testing.T) {
	store, pool := newMockStore(t)
	h := NewHandler(store) // no emailer

	// Only the two Complete execs — NO GetByID load.
	pool.ExpectExec(`UPDATE cycles SET status = 'completed'`).
		WithArgs("c1").
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	pool.ExpectExec(`UPDATE issues SET cycle_id = NULL`).
		WithArgs("c1").
		WillReturnResult(pgxmock.NewResult("UPDATE", 2))

	req := reqWithID(http.MethodPost, "c1", "")
	w := httptest.NewRecorder()
	h.Complete(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	if err := pool.ExpectationsWereMet(); err != nil {
		t.Errorf("db expectations (no extra GetByID load expected): %v", err)
	}
}
