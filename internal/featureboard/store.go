// Package featureboard implements public feature-request boards.
//
// Boards live under a workspace and expose a public URL (no auth)
// where users can submit ideas and vote. Votes dedupe by email —
// no IP heuristics, no fingerprinting; users who want to vote twice
// just need a second mailbox. Rate-limiting is in-memory and per
// (board, email) tuple — fine for the per-process model Track ships
// today; the production stack can swap in Redis later via the
// PostRateLimiter interface without touching the SQL layer.
package featureboard

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/talyvor/track/internal/tenancy"
)

// ─── public types ───────────────────────────────────────────

type PostStatus string

const (
	PostStatusOpen       PostStatus = "open"
	PostStatusPlanned    PostStatus = "planned"
	PostStatusInProgress PostStatus = "in_progress"
	PostStatusCompleted  PostStatus = "completed"
	PostStatusDeclined   PostStatus = "declined"
)

var validStatuses = map[PostStatus]struct{}{
	PostStatusOpen:       {},
	PostStatusPlanned:    {},
	PostStatusInProgress: {},
	PostStatusCompleted:  {},
	PostStatusDeclined:   {},
}

type Board struct {
	ID             string    `json:"id"`
	WorkspaceID    string    `json:"workspace_id"`
	Name           string    `json:"name"`
	Description    string    `json:"description"`
	Slug           string    `json:"slug"`
	Public         bool      `json:"public"`
	AllowAnonymous bool      `json:"allow_anonymous"`
	CreatedAt      time.Time `json:"created_at"`
}

type FeaturePost struct {
	ID          string     `json:"id"`
	WorkspaceID string     `json:"workspace_id"`
	BoardID     string     `json:"board_id"`
	Title       string     `json:"title"`
	Description string     `json:"description"`
	Status      PostStatus `json:"status"`
	VoteCount   int        `json:"vote_count"`
	IssueID     *string    `json:"issue_id,omitempty"`
	AuthorName  string     `json:"author_name"`
	AuthorEmail string     `json:"author_email"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
}

type BoardStats struct {
	TotalPosts int            `json:"total_posts"`
	TotalVotes int            `json:"total_votes"`
	ByStatus   map[string]int `json:"by_status"`
	TopPost    *FeaturePost   `json:"top_post,omitempty"`
}

// ─── slug + sanitization helpers ────────────────────────────

// slugRe accepts a-z0-9 + hyphens, with no leading/trailing hyphen.
var slugRe = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)

// slugify maps a free-form name into a URL-safe slug. Non-ASCII
// characters are dropped silently — produces predictable output for
// the default "generate slug from name" path used by CreateBoard.
func slugify(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	// Replace non-alphanumeric with hyphens, then collapse runs.
	var b strings.Builder
	prevHyphen := true // suppress leading hyphen
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			prevHyphen = false
		case r == ' ' || r == '-' || r == '_':
			if !prevHyphen {
				b.WriteRune('-')
				prevHyphen = true
			}
		}
	}
	out := strings.TrimRight(b.String(), "-")
	if out == "" {
		out = "board"
	}
	return out
}

// stripHTML removes HTML markup from user-submitted text. Script /
// style blocks have their contents removed entirely (otherwise the
// "alert(1)" body of a <script> tag would survive); other tags are
// removed leaving their inner text intact. The frontend never
// renders the result as HTML — this is defense-in-depth.
//
// Go's regexp (RE2) doesn't support backreferences, so we run two
// per-tag scrubbers instead of one with a capture group.
var (
	scriptBlockRe = regexp.MustCompile(`(?is)<script[^>]*>.*?</script>`)
	styleBlockRe  = regexp.MustCompile(`(?is)<style[^>]*>.*?</style>`)
	htmlRe        = regexp.MustCompile(`<[^>]*>`)
)

func stripHTML(s string) string {
	s = scriptBlockRe.ReplaceAllString(s, "")
	s = styleBlockRe.ReplaceAllString(s, "")
	return strings.TrimSpace(htmlRe.ReplaceAllString(s, ""))
}

// ─── rate limiter ───────────────────────────────────────────

const (
	rateWindow      = 24 * time.Hour
	maxPostsPerUser = 3
)

// rateLimiter is the per-process in-memory bucket. We map (board,
// email) → recent timestamps and prune entries older than the
// window on each check. Acceptable for the per-board volume Track
// targets; swap for Redis later if a single board sustains > 10 RPS.
type rateLimiter struct {
	mu sync.Mutex
	by map[string][]time.Time
}

func newRateLimiter() *rateLimiter { return &rateLimiter{by: map[string][]time.Time{}} }

// allow returns true and records the timestamp if the key is below
// the per-window cap; otherwise returns false and records nothing.
// The check + record are inside the same lock so concurrent posts
// can't slip past the cap.
func (r *rateLimiter) allow(key string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now()
	kept := r.by[key][:0]
	for _, t := range r.by[key] {
		if now.Sub(t) < rateWindow {
			kept = append(kept, t)
		}
	}
	if len(kept) >= maxPostsPerUser {
		r.by[key] = kept
		return false
	}
	r.by[key] = append(kept, now)
	return true
}

// ─── store ──────────────────────────────────────────────────

type pgxDB interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Begin(ctx context.Context) (pgx.Tx, error)
}

type Store struct {
	pool    pgxDB
	limiter *rateLimiter
}

func NewStore(pool *pgxpool.Pool) *Store {
	var db pgxDB
	if pool != nil {
		db = pool
	}
	return newStore(db)
}

func newStore(db pgxDB) *Store {
	return &Store{pool: db, limiter: newRateLimiter()}
}

const boardColumns = `id, workspace_id, name, description, slug,
    public, allow_anonymous, created_at`

func scanBoard(s interface{ Scan(...any) error }) (*Board, error) {
	var b Board
	if err := s.Scan(
		&b.ID, &b.WorkspaceID, &b.Name, &b.Description, &b.Slug,
		&b.Public, &b.AllowAnonymous, &b.CreatedAt,
	); err != nil {
		return nil, err
	}
	return &b, nil
}

const postColumns = `id, workspace_id, board_id, title, description, status,
    vote_count, issue_id, author_name, author_email, created_at, updated_at`

func scanPost(s interface{ Scan(...any) error }) (*FeaturePost, error) {
	var (
		p      FeaturePost
		status string
	)
	if err := s.Scan(
		&p.ID, &p.WorkspaceID, &p.BoardID, &p.Title, &p.Description, &status,
		&p.VoteCount, &p.IssueID, &p.AuthorName, &p.AuthorEmail,
		&p.CreatedAt, &p.UpdatedAt,
	); err != nil {
		return nil, err
	}
	p.Status = PostStatus(status)
	return &p, nil
}

// ─── CreateBoard ───────────────────────────────────────────

func (s *Store) CreateBoard(ctx context.Context, b Board) (*Board, error) {
	if s.pool == nil {
		return nil, errors.New("featureboard: store has no pool")
	}
	if strings.TrimSpace(b.Name) == "" {
		return nil, errors.New("featureboard: name required")
	}
	if b.Slug == "" {
		b.Slug = slugify(b.Name)
	}
	if !slugRe.MatchString(b.Slug) {
		return nil, fmt.Errorf("featureboard: invalid slug %q (lowercase, alphanumeric + hyphens)", b.Slug)
	}
	return scanBoard(s.pool.QueryRow(ctx,
		`INSERT INTO feature_boards (workspace_id, name, description, slug, public, allow_anonymous)
        VALUES ($1, $2, $3, $4, $5, $6) RETURNING `+boardColumns,
		b.WorkspaceID, strings.TrimSpace(b.Name), b.Description,
		b.Slug, b.Public, b.AllowAnonymous,
	))
}

// ─── GetBoard ──────────────────────────────────────────────

func (s *Store) GetBoard(ctx context.Context, slug, workspaceID string) (*Board, error) {
	if s.pool == nil {
		return nil, errors.New("featureboard: store has no pool")
	}
	return scanBoard(s.pool.QueryRow(ctx,
		`SELECT `+boardColumns+` FROM feature_boards
        WHERE slug = $1 AND workspace_id = $2`,
		slug, workspaceID,
	))
}

// GetPublicBoard looks up a board by workspace slug + board slug.
// Used by the public /v1/public/boards/:wsSlug/:boardSlug endpoints
// so the URL doesn't have to leak workspace UUIDs. The query refuses
// non-public boards so a private workspace can host a single public
// board without exposing the others.
func (s *Store) GetPublicBoard(ctx context.Context, wsSlug, boardSlug string) (*Board, error) {
	if s.pool == nil {
		return nil, errors.New("featureboard: store has no pool")
	}
	return scanBoard(s.pool.QueryRow(ctx,
		`SELECT b.id, b.workspace_id, b.name, b.description, b.slug,
                b.public, b.allow_anonymous, b.created_at
            FROM feature_boards b
            JOIN workspaces w ON w.id = b.workspace_id
            WHERE w.slug = $1 AND b.slug = $2 AND b.public = true`,
		wsSlug, boardSlug,
	))
}

// ListBoards returns all boards for a workspace. Used by the admin
// page; ordered by creation date so the freshest board is on top.
func (s *Store) ListBoards(ctx context.Context, workspaceID string) ([]Board, error) {
	if s.pool == nil {
		return nil, nil
	}
	rows, err := s.pool.Query(ctx,
		`SELECT `+boardColumns+` FROM feature_boards
        WHERE workspace_id = $1 ORDER BY created_at DESC`,
		workspaceID,
	)
	if err != nil {
		return nil, fmt.Errorf("featureboard: list boards: %w", err)
	}
	defer rows.Close()
	var out []Board
	for rows.Next() {
		b, err := scanBoard(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *b)
	}
	return out, rows.Err()
}

// ─── CreatePost ────────────────────────────────────────────

func (s *Store) CreatePost(ctx context.Context, p FeaturePost) (*FeaturePost, error) {
	if s.pool == nil {
		return nil, errors.New("featureboard: store has no pool")
	}
	p.Title = stripHTML(p.Title)
	p.Description = stripHTML(p.Description)
	if p.Title == "" {
		return nil, errors.New("featureboard: title required")
	}
	if p.AuthorName == "" {
		p.AuthorName = "Anonymous"
	}
	// Rate-limit on (board, email) so a bad actor needs a fresh
	// mailbox per burst. Empty email (truly anonymous) is rate-
	// limited globally on (board, "").
	if !s.limiter.allow(p.BoardID + ":" + p.AuthorEmail) {
		return nil, fmt.Errorf("featureboard: rate limit — max %d posts per email per 24h", maxPostsPerUser)
	}
	if err := tenancy.AssertRefInWorkspace(ctx, s.pool, "feature_boards", p.BoardID, p.WorkspaceID); err != nil {
		return nil, err
	}
	return scanPost(s.pool.QueryRow(ctx,
		`INSERT INTO feature_posts
            (workspace_id, board_id, title, description, author_name, author_email)
        VALUES ($1, $2, $3, $4, $5, $6) RETURNING `+postColumns,
		p.WorkspaceID, p.BoardID, p.Title, p.Description, p.AuthorName, p.AuthorEmail,
	))
}

// ─── ListPosts ─────────────────────────────────────────────

func (s *Store) ListPosts(ctx context.Context, boardID string, status *PostStatus, orderBy string) ([]FeaturePost, error) {
	if s.pool == nil {
		return nil, nil
	}
	args := []any{boardID}
	whereStatus := ""
	if status != nil {
		whereStatus = " AND status = $2"
		args = append(args, string(*status))
	}
	order := "vote_count DESC, created_at DESC"
	switch orderBy {
	case "newest":
		order = "created_at DESC"
	case "oldest":
		order = "created_at ASC"
	}
	rows, err := s.pool.Query(ctx,
		`SELECT `+postColumns+` FROM feature_posts WHERE board_id = $1`+whereStatus+
			" ORDER BY "+order,
		args...,
	)
	if err != nil {
		return nil, fmt.Errorf("featureboard: list posts: %w", err)
	}
	defer rows.Close()
	var out []FeaturePost
	for rows.Next() {
		p, err := scanPost(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}

// GetPost returns one post by ID — used by the admin status-change
// endpoint and by the "convert to issue" flow.
func (s *Store) GetPost(ctx context.Context, postID string) (*FeaturePost, error) {
	if s.pool == nil {
		return nil, errors.New("featureboard: store has no pool")
	}
	return scanPost(s.pool.QueryRow(ctx,
		`SELECT `+postColumns+` FROM feature_posts WHERE id = $1`,
		postID,
	))
}

// ─── Vote / Unvote ─────────────────────────────────────────

// assertPostOnPublicBoard enforces object-graph integrity for the public vote
// path: the post must belong to a PUBLIC board whose workspace-slug and board-slug
// match the ones in the request URL. Without it, Vote/Unvote mutate any post by
// bare ID — including posts on unpublished boards or under a different board.
func (s *Store) assertPostOnPublicBoard(ctx context.Context, wsSlug, boardSlug, postID string) error {
	var ok bool
	if err := s.pool.QueryRow(ctx,
		`SELECT EXISTS(
            SELECT 1 FROM feature_posts p
            JOIN feature_boards b ON b.id = p.board_id
            JOIN workspaces w ON w.id = b.workspace_id
            WHERE p.id = $1 AND b.slug = $2 AND w.slug = $3 AND b.public = true)`,
		postID, boardSlug, wsSlug,
	).Scan(&ok); err != nil {
		return fmt.Errorf("featureboard: board check: %w", err)
	}
	if !ok {
		return errors.New("featureboard: post does not belong to the published board in the URL")
	}
	return nil
}

// Vote inserts the (post, email) record and re-syncs vote_count. The
// dedupe + count refresh sit inside one transaction so concurrent
// votes can't double-count or under-count.
func (s *Store) Vote(ctx context.Context, wsSlug, boardSlug, postID, email string) (int, error) {
	if s.pool == nil {
		return 0, errors.New("featureboard: store has no pool")
	}
	if postID == "" || email == "" {
		return 0, errors.New("featureboard: post_id and email required")
	}
	if err := s.assertPostOnPublicBoard(ctx, wsSlug, boardSlug, postID); err != nil {
		return 0, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("featureboard: vote begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx,
		`INSERT INTO feature_votes (post_id, email) VALUES ($1, $2)
        ON CONFLICT (post_id, email) DO NOTHING`,
		postID, strings.ToLower(strings.TrimSpace(email)),
	); err != nil {
		return 0, fmt.Errorf("featureboard: vote insert: %w", err)
	}

	var count int64
	// nosemgrep: operate-by-id-write-requires-workspace-scope -- public feedback board: assertPostOnPublicBoard(wsSlug, boardSlug, postID) above proves the post is on the named public board before this recount. Votes are email-identified, not workspace-membership — there is no authorized workspace to scope to. INVALIDATED IF the assertPostOnPublicBoard(wsSlug, boardSlug, postID) guard is removed or moved BELOW this recount (the post must be proven on the named public board before the by-id write).
	if err := tx.QueryRow(ctx,
		`UPDATE feature_posts
        SET vote_count = (SELECT COUNT(*) FROM feature_votes WHERE post_id = $1),
            updated_at = NOW()
        WHERE id = $1 RETURNING vote_count`,
		postID,
	).Scan(&count); err != nil {
		return 0, fmt.Errorf("featureboard: vote update: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("featureboard: vote commit: %w", err)
	}
	return int(count), nil
}

// Unvote removes the vote and re-syncs vote_count. Same transaction
// shape as Vote so the count is always consistent.
func (s *Store) Unvote(ctx context.Context, wsSlug, boardSlug, postID, email string) (int, error) {
	if s.pool == nil {
		return 0, errors.New("featureboard: store has no pool")
	}
	if postID == "" || email == "" {
		return 0, errors.New("featureboard: post_id and email required")
	}
	if err := s.assertPostOnPublicBoard(ctx, wsSlug, boardSlug, postID); err != nil {
		return 0, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("featureboard: unvote begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx,
		`DELETE FROM feature_votes WHERE post_id = $1 AND email = $2`,
		postID, strings.ToLower(strings.TrimSpace(email)),
	); err != nil {
		return 0, fmt.Errorf("featureboard: unvote delete: %w", err)
	}
	var count int64
	// nosemgrep: operate-by-id-write-requires-workspace-scope -- public feedback board: assertPostOnPublicBoard(wsSlug, boardSlug, postID) above proves the post is on the named public board before this recount. Votes are email-identified, not workspace-membership — there is no authorized workspace to scope to. INVALIDATED IF the assertPostOnPublicBoard(wsSlug, boardSlug, postID) guard is removed or moved BELOW this recount (the post must be proven on the named public board before the by-id write).
	if err := tx.QueryRow(ctx,
		`UPDATE feature_posts
        SET vote_count = (SELECT COUNT(*) FROM feature_votes WHERE post_id = $1),
            updated_at = NOW()
        WHERE id = $1 RETURNING vote_count`,
		postID,
	).Scan(&count); err != nil {
		return 0, fmt.Errorf("featureboard: unvote update: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("featureboard: unvote commit: %w", err)
	}
	return int(count), nil
}

// HasVoted is the public-board read-time check that powers the
// "you've already voted" UI. EXISTS keeps the query single-key — no
// scan + count overhead.
func (s *Store) HasVoted(ctx context.Context, postID, email string) (bool, error) {
	if s.pool == nil {
		return false, nil
	}
	var voted bool
	err := s.pool.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM feature_votes WHERE post_id = $1 AND email = $2)`,
		postID, strings.ToLower(strings.TrimSpace(email)),
	).Scan(&voted)
	if err != nil {
		return false, fmt.Errorf("featureboard: has_voted: %w", err)
	}
	return voted, nil
}

// ─── UpdateStatus ──────────────────────────────────────────

// UpdateStatus changes a post's status and optionally links it to a
// Track issue. issueID nil leaves the existing link untouched ONLY
// when callers explicitly pass nil — that matches the spec's "link
// is optional" intent (the handler always provides one when the
// admin form submits an issue choice).
func (s *Store) UpdateStatus(ctx context.Context, wsID, boardID, postID string, status PostStatus, issueID *string) error {
	if s.pool == nil {
		return errors.New("featureboard: store has no pool")
	}
	if _, ok := validStatuses[status]; !ok {
		return fmt.Errorf("featureboard: invalid status %q", status)
	}
	// Object-graph integrity: a linked issue must share the post's workspace.
	if issueID != nil && *issueID != "" {
		var ok bool
		if err := s.pool.QueryRow(ctx,
			`SELECT EXISTS(SELECT 1 FROM issues WHERE id = $1 AND workspace_id = $2)`,
			*issueID, wsID,
		).Scan(&ok); err != nil {
			return fmt.Errorf("featureboard: validate linked issue: %w", err)
		}
		if !ok {
			return errors.New("featureboard: linked issue is in a different workspace")
		}
	}
	// Scope the mutation to the post's own board + workspace; a post that doesn't
	// belong there is refused (0 rows affected).
	tag, err := s.pool.Exec(ctx,
		`UPDATE feature_posts SET status = $1, issue_id = $2, updated_at = NOW()
        WHERE id = $3 AND workspace_id = $4 AND board_id = $5`,
		string(status), issueID, postID, wsID, boardID,
	)
	if err != nil {
		return fmt.Errorf("featureboard: update status: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return errors.New("featureboard: post not found on the expected board/workspace")
	}
	return nil
}

// ─── GetStats ──────────────────────────────────────────────

func (s *Store) GetStats(ctx context.Context, boardID, workspaceID string) (*BoardStats, error) {
	if s.pool == nil {
		return &BoardStats{ByStatus: map[string]int{}}, nil
	}
	// SEC-5: only expose stats for a board in the caller's workspace — a foreign board id returns
	// empty stats (no cross-tenant disclosure, no existence oracle).
	var inWS bool
	if err := s.pool.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM feature_boards WHERE id = $1 AND workspace_id = $2)`, boardID, workspaceID).Scan(&inWS); err != nil {
		return nil, fmt.Errorf("featureboard: stats scope: %w", err)
	}
	if !inWS {
		return &BoardStats{ByStatus: map[string]int{}}, nil
	}
	out := &BoardStats{ByStatus: map[string]int{}}

	var total, votes int64
	if err := s.pool.QueryRow(ctx,
		`SELECT COUNT(*), COALESCE(SUM(vote_count), 0) FROM feature_posts WHERE board_id = $1`,
		boardID,
	).Scan(&total, &votes); err != nil {
		return nil, fmt.Errorf("featureboard: stats totals: %w", err)
	}
	out.TotalPosts = int(total)
	out.TotalVotes = int(votes)

	rows, err := s.pool.Query(ctx,
		`SELECT status, COUNT(*) FROM feature_posts WHERE board_id = $1 GROUP BY status`,
		boardID,
	)
	if err != nil {
		return nil, fmt.Errorf("featureboard: stats by status: %w", err)
	}
	for rows.Next() {
		var (
			status string
			n      int64
		)
		if err := rows.Scan(&status, &n); err != nil {
			rows.Close()
			return nil, err
		}
		out.ByStatus[status] = int(n)
	}
	rows.Close()

	// Top post is informational — missing-row error is fine.
	top, err := scanPost(s.pool.QueryRow(ctx,
		`SELECT `+postColumns+` FROM feature_posts
        WHERE board_id = $1 ORDER BY vote_count DESC LIMIT 1`,
		boardID,
	))
	if err == nil {
		out.TopPost = top
	}
	return out, nil
}
