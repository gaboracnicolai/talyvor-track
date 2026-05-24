package featureboard

import (
	"context"
	"testing"
	"time"

	"github.com/pashagolub/pgxmock/v4"
)

func newMockStore(t *testing.T) (*Store, pgxmock.PgxPoolIface) {
	t.Helper()
	pool, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock.NewPool: %v", err)
	}
	t.Cleanup(pool.Close)
	return newStore(pool), pool
}

func ptr[T any](v T) *T { return &v }

// row builders --------------------------------------------------------

func boardRows() *pgxmock.Rows {
	return pgxmock.NewRows([]string{
		"id", "workspace_id", "name", "description", "slug",
		"public", "allow_anonymous", "created_at",
	})
}

func postRows() *pgxmock.Rows {
	return pgxmock.NewRows([]string{
		"id", "workspace_id", "board_id", "title", "description", "status",
		"vote_count", "issue_id", "author_name", "author_email",
		"created_at", "updated_at",
	})
}

// ─── slugify ────────────────────────────────────────────────

func TestSlugify_Strict(t *testing.T) {
	cases := []struct{ in, want string }{
		{"Hello World", "hello-world"},
		{"Q3 Launch!", "q3-launch"},
		{"  Spaces   trimmed  ", "spaces-trimmed"},
		{"©™ unicode ", "unicode"},
	}
	for _, c := range cases {
		if got := slugify(c.in); got != c.want {
			t.Errorf("slugify(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// ─── CreateBoard ───────────────────────────────────────────

func TestCreateBoard_GeneratesSlugFromName(t *testing.T) {
	store, pool := newMockStore(t)
	now := time.Now().UTC()
	pool.ExpectQuery(`INSERT INTO feature_boards`).
		WithArgs("ws-1", "Public Roadmap", "", "public-roadmap", true, true).
		WillReturnRows(boardRows().AddRow(
			"b-1", "ws-1", "Public Roadmap", "", "public-roadmap", true, true, now,
		))
	out, err := store.CreateBoard(context.Background(), Board{
		WorkspaceID: "ws-1", Name: "Public Roadmap", Public: true, AllowAnonymous: true,
	})
	if err != nil {
		t.Fatalf("CreateBoard: %v", err)
	}
	if out.Slug != "public-roadmap" {
		t.Errorf("slug = %q", out.Slug)
	}
}

func TestCreateBoard_RejectsInvalidSlug(t *testing.T) {
	store, _ := newMockStore(t)
	_, err := store.CreateBoard(context.Background(), Board{
		WorkspaceID: "ws-1", Name: "x", Slug: "bad slug!",
	})
	if err == nil {
		t.Fatal("expected error for invalid slug")
	}
}

// ─── CreatePost ─────────────────────────────────────────────

func TestCreatePost_StripsHTML(t *testing.T) {
	store, pool := newMockStore(t)
	now := time.Now().UTC()
	pool.ExpectQuery(`INSERT INTO feature_posts`).
		WithArgs("ws-1", "b-1", "Add dark mode", "Use OLED-friendly colours", "Alice", "a@example.com").
		WillReturnRows(postRows().AddRow(
			"p-1", "ws-1", "b-1", "Add dark mode", "Use OLED-friendly colours", "open",
			0, (*string)(nil), "Alice", "a@example.com", now, now,
		))

	out, err := store.CreatePost(context.Background(), FeaturePost{
		WorkspaceID: "ws-1",
		BoardID:     "b-1",
		Title:       "<script>alert(1)</script>Add dark mode",
		Description: "Use <b>OLED</b>-friendly <em>colours</em>",
		AuthorName:  "Alice",
		AuthorEmail: "a@example.com",
	})
	if err != nil {
		t.Fatalf("CreatePost: %v", err)
	}
	if out.Title != "Add dark mode" {
		t.Errorf("title = %q (should be HTML-stripped)", out.Title)
	}
}

func TestCreatePost_RateLimits(t *testing.T) {
	store, pool := newMockStore(t)
	now := time.Now().UTC()
	// Three successful posts in a row, then the fourth gets rate-
	// limited before any SQL fires.
	for i := 0; i < 3; i++ {
		pool.ExpectQuery(`INSERT INTO feature_posts`).
			WithArgs("ws-1", "b-1", "Idea", "", "Alice", "rate@example.com").
			WillReturnRows(postRows().AddRow(
				"p", "ws-1", "b-1", "Idea", "", "open",
				0, (*string)(nil), "Alice", "rate@example.com", now, now,
			))
	}
	for i := 0; i < 3; i++ {
		if _, err := store.CreatePost(context.Background(), FeaturePost{
			WorkspaceID: "ws-1", BoardID: "b-1", Title: "Idea",
			AuthorName: "Alice", AuthorEmail: "rate@example.com",
		}); err != nil {
			t.Fatalf("post %d: %v", i, err)
		}
	}
	_, err := store.CreatePost(context.Background(), FeaturePost{
		WorkspaceID: "ws-1", BoardID: "b-1", Title: "Idea",
		AuthorName: "Alice", AuthorEmail: "rate@example.com",
	})
	if err == nil {
		t.Fatal("expected rate-limit error on 4th post within 24h")
	}
}

// ─── Vote / Unvote ──────────────────────────────────────────

func TestVote_IncrementsAndDedupes(t *testing.T) {
	store, pool := newMockStore(t)
	// INSERT vote, then refresh count via UPDATE … RETURNING. Wrap
	// in a transaction so the dedupe + count stay consistent.
	pool.ExpectBegin()
	pool.ExpectExec(`INSERT INTO feature_votes`).
		WithArgs("p-1", "alice@example.com").
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	pool.ExpectQuery(`UPDATE feature_posts SET vote_count`).
		WithArgs("p-1").
		WillReturnRows(pgxmock.NewRows([]string{"vote_count"}).AddRow(int64(1)))
	pool.ExpectCommit()

	count, err := store.Vote(context.Background(), "p-1", "alice@example.com")
	if err != nil {
		t.Fatalf("Vote: %v", err)
	}
	if count != 1 {
		t.Errorf("count = %d", count)
	}
}

func TestUnvote_Decrements(t *testing.T) {
	store, pool := newMockStore(t)
	pool.ExpectBegin()
	pool.ExpectExec(`DELETE FROM feature_votes`).
		WithArgs("p-1", "alice@example.com").
		WillReturnResult(pgxmock.NewResult("DELETE", 1))
	pool.ExpectQuery(`UPDATE feature_posts SET vote_count`).
		WithArgs("p-1").
		WillReturnRows(pgxmock.NewRows([]string{"vote_count"}).AddRow(int64(0)))
	pool.ExpectCommit()

	count, err := store.Unvote(context.Background(), "p-1", "alice@example.com")
	if err != nil {
		t.Fatalf("Unvote: %v", err)
	}
	if count != 0 {
		t.Errorf("count = %d", count)
	}
}

func TestHasVoted_ReturnsCorrectBool(t *testing.T) {
	store, pool := newMockStore(t)
	pool.ExpectQuery(`SELECT EXISTS`).
		WithArgs("p-1", "a@example.com").
		WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(true))
	yes, err := store.HasVoted(context.Background(), "p-1", "a@example.com")
	if err != nil || !yes {
		t.Errorf("yes path: voted=%v err=%v", yes, err)
	}
}

// ─── ListPosts ──────────────────────────────────────────────

func TestListPosts_OrderedByVotes(t *testing.T) {
	store, pool := newMockStore(t)
	now := time.Now().UTC()
	pool.ExpectQuery(`ORDER BY vote_count DESC`).
		WithArgs("b-1").
		WillReturnRows(postRows().
			AddRow("p-1", "ws-1", "b-1", "Hot", "", "open",
				50, (*string)(nil), "x", "x@x.com", now, now).
			AddRow("p-2", "ws-1", "b-1", "Cold", "", "open",
				2, (*string)(nil), "x", "x@x.com", now, now))

	out, err := store.ListPosts(context.Background(), "b-1", nil, "votes")
	if err != nil {
		t.Fatalf("ListPosts: %v", err)
	}
	if out[0].VoteCount != 50 {
		t.Errorf("first vote_count = %d", out[0].VoteCount)
	}
}

func TestListPosts_StatusFilter(t *testing.T) {
	store, pool := newMockStore(t)
	now := time.Now().UTC()
	pool.ExpectQuery(`AND status = \$2`).
		WithArgs("b-1", "planned").
		WillReturnRows(postRows().AddRow(
			"p-1", "ws-1", "b-1", "Planned thing", "", "planned",
			10, (*string)(nil), "x", "x@x.com", now, now,
		))
	status := PostStatusPlanned
	out, err := store.ListPosts(context.Background(), "b-1", &status, "votes")
	if err != nil {
		t.Fatalf("ListPosts: %v", err)
	}
	if len(out) != 1 || out[0].Status != PostStatusPlanned {
		t.Errorf("got %+v", out)
	}
}

// ─── UpdateStatus ───────────────────────────────────────────

func TestUpdateStatus_LinksIssue(t *testing.T) {
	store, pool := newMockStore(t)
	pool.ExpectExec(`UPDATE feature_posts SET status`).
		WithArgs("planned", ptr("i-99"), "p-1").
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	if err := store.UpdateStatus(context.Background(), "p-1", PostStatusPlanned, ptr("i-99")); err != nil {
		t.Fatalf("UpdateStatus: %v", err)
	}
}

// ─── GetStats ───────────────────────────────────────────────

func TestGetStats_ReturnsCounts(t *testing.T) {
	store, pool := newMockStore(t)
	now := time.Now().UTC()
	pool.ExpectQuery(`SELECT COUNT.*SUM.*FROM feature_posts`).
		WithArgs("b-1").
		WillReturnRows(pgxmock.NewRows([]string{"total_posts", "total_votes"}).
			AddRow(int64(20), int64(150)))
	pool.ExpectQuery(`GROUP BY status`).
		WithArgs("b-1").
		WillReturnRows(pgxmock.NewRows([]string{"status", "count"}).
			AddRow("open", int64(12)).
			AddRow("planned", int64(5)).
			AddRow("completed", int64(3)))
	pool.ExpectQuery(`ORDER BY vote_count DESC LIMIT 1`).
		WithArgs("b-1").
		WillReturnRows(postRows().AddRow(
			"p-top", "ws-1", "b-1", "Top", "", "open",
			42, (*string)(nil), "x", "x@x.com", now, now,
		))

	out, err := store.GetStats(context.Background(), "b-1")
	if err != nil {
		t.Fatalf("GetStats: %v", err)
	}
	if out.TotalPosts != 20 || out.TotalVotes != 150 {
		t.Errorf("counts = %+v", out)
	}
	if out.ByStatus["open"] != 12 {
		t.Errorf("ByStatus open = %d", out.ByStatus["open"])
	}
	if out.TopPost == nil || out.TopPost.ID != "p-top" {
		t.Errorf("top = %+v", out.TopPost)
	}
}
