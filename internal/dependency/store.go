// Package dependency models typed, directed relationships between
// issues — blocks, duplicates, relates_to, clones.
//
// blocks ↔ blocked_by and duplicates ↔ duplicates are stored as two
// rows so a single index lookup answers "what's related to X" from
// either side. relates_to and clones are intrinsically directional
// and stored as a single row; GetRelations surfaces them from the
// target's perspective on read.
package dependency

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/talyvor/track/internal/model"
)

// ─── public types ───────────────────────────────────────────

type RelationType string

const (
	RelationBlocks     RelationType = "blocks"
	RelationBlockedBy  RelationType = "blocked_by"
	RelationRelates    RelationType = "relates_to"
	RelationDuplicates RelationType = "duplicates"
	RelationClones     RelationType = "clones"
)

// MaxGraphDepth caps BFS expansion in GetDependencyGraph so a
// pathological web of relations can't blow up the response. 5 is
// generous — most teams operate on 1-2 hops.
const MaxGraphDepth = 5

type Relation struct {
	ID          string       `json:"id"`
	SourceID    string       `json:"source_id"`
	TargetID    string       `json:"target_id"`
	Type        RelationType `json:"type"`
	WorkspaceID string       `json:"workspace_id"`
	CreatedBy   string       `json:"created_by"`
	CreatedAt   time.Time    `json:"created_at"`
}

// RelationWithIssue is GetRelations's wire shape: the relation plus
// the *other* issue (i.e. the one that isn't the perspective issue).
type RelationWithIssue struct {
	Relation
	Issue model.Issue `json:"issue"`
}

type DependencyGraph struct {
	Nodes []GraphNode `json:"nodes"`
	Edges []GraphEdge `json:"edges"`
}

type GraphNode struct {
	ID         string `json:"id"`
	Identifier string `json:"identifier"`
	Title      string `json:"title"`
	Status     string `json:"status"`
	IsBlocked  bool   `json:"is_blocked"`
}

type GraphEdge struct {
	Source string       `json:"source"`
	Target string       `json:"target"`
	Type   RelationType `json:"type"`
}

// ─── store internals ────────────────────────────────────────

type pgxDB interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

type Store struct{ pool pgxDB }

func NewStore(pool *pgxpool.Pool) *Store {
	var db pgxDB
	if pool != nil {
		db = pool
	}
	return newStore(db)
}

func newStore(db pgxDB) *Store { return &Store{pool: db} }

// validTypes is the closed set of relation kinds. Anything else gets
// rejected at the boundary so caller bugs don't pollute the table.
var validTypes = map[RelationType]struct{}{
	RelationBlocks:     {},
	RelationBlockedBy:  {},
	RelationRelates:    {},
	RelationDuplicates: {},
	RelationClones:     {},
}

// hasAutoInverse marks the relation kinds we mirror as a second row
// on create / delete. blocks ↔ blocked_by give us a symmetric lookup
// without a UNION; duplicates is intrinsically symmetric.
func hasAutoInverse(t RelationType) bool {
	switch t {
	case RelationBlocks, RelationBlockedBy, RelationDuplicates:
		return true
	}
	return false
}

func inverseType(t RelationType) RelationType {
	switch t {
	case RelationBlocks:
		return RelationBlockedBy
	case RelationBlockedBy:
		return RelationBlocks
	case RelationDuplicates:
		return RelationDuplicates
	case RelationRelates:
		return RelationRelates
	case RelationClones:
		return RelationClones // "is cloned by" from target's POV; same constant
	}
	return t
}

const relationColumns = `id, source_id, target_id, type, workspace_id, created_by, created_at`

// ─── Create ─────────────────────────────────────────────────

// Create inserts the relation and, for symmetric types, the inverse
// row. The inverse uses ON CONFLICT DO NOTHING so an existing inverse
// (from an earlier create that crashed mid-way) is a no-op instead
// of a hard failure.
// assertIssuesShareWorkspace refuses unless both issues exist and share a workspace.
// Object-graph integrity: a relation must never link issues across a workspace
// boundary. EXISTS returns a plain bool (false if either issue is missing or they
// differ), so there is no nullable-scan ambiguity.
func (s *Store) assertIssuesShareWorkspace(ctx context.Context, aID, bID string) error {
	var ok bool
	if err := s.pool.QueryRow(ctx,
		`SELECT EXISTS(
            SELECT 1 FROM issues a JOIN issues b ON a.workspace_id = b.workspace_id
            WHERE a.id = $1 AND b.id = $2)`,
		aID, bID,
	).Scan(&ok); err != nil {
		return fmt.Errorf("dependency: workspace check: %w", err)
	}
	if !ok {
		return errors.New("dependency: source and target issues are in different workspaces (or missing)")
	}
	return nil
}

// ErrCycle is returned when a blocks-family relation would close a directed cycle in
// the blocks graph (A blocks B when B already transitively blocks A) — a dependency
// deadlock. Handlers map it to 409 Conflict.
var ErrCycle = errors.New("dependency: relation would create a blocking cycle")

// blocksEdge normalises a blocks-family relation to its (blocker, blocked) issue IDs —
// blocker must complete before blocked. Non-dependency relations (relates_to,
// duplicates, clones) return ok=false; they cannot form a blocking cycle.
func blocksEdge(t RelationType, source, target string) (blocker, blocked string, ok bool) {
	switch t {
	case RelationBlocks:
		return source, target, true
	case RelationBlockedBy:
		return target, source, true
	}
	return "", "", false
}

// wouldCloseBlockingCycle reports whether adding the relation would close a directed
// cycle. It walks the blocks graph (recursive, deduped — so it terminates even if the
// data already contains a cycle) asking whether `blocked` can already reach `blocker`;
// if so, the new blocker→blocked edge completes a cycle. Transitive: A→B→C→A is caught,
// while a diamond (A→B, A→C, B→D, C→D) is not — no back-path exists.
func (s *Store) wouldCloseBlockingCycle(ctx context.Context, t RelationType, source, target, workspaceID string) (bool, error) {
	blocker, blocked, ok := blocksEdge(t, source, target)
	if !ok {
		return false, nil
	}
	var reaches bool
	err := s.pool.QueryRow(ctx, `
        WITH RECURSIVE reachable AS (
            SELECT target_id AS node FROM issue_relations
            WHERE source_id = $1 AND type = 'blocks' AND workspace_id = $3
          UNION
            SELECT r.target_id FROM issue_relations r
            JOIN reachable rc ON r.source_id = rc.node
            WHERE r.type = 'blocks' AND r.workspace_id = $3
        )
        SELECT EXISTS (SELECT 1 FROM reachable WHERE node = $2)`,
		blocked, blocker, workspaceID,
	).Scan(&reaches)
	if err != nil {
		return false, fmt.Errorf("dependency: cycle check: %w", err)
	}
	return reaches, nil
}

func (s *Store) Create(ctx context.Context, r Relation) (*Relation, error) {
	if s.pool == nil {
		return nil, errors.New("dependency: store has no pool")
	}
	if r.SourceID == "" || r.TargetID == "" {
		return nil, errors.New("dependency: source_id and target_id required")
	}
	if r.SourceID == r.TargetID {
		return nil, errors.New("dependency: cannot relate an issue to itself")
	}
	if _, ok := validTypes[r.Type]; !ok {
		return nil, fmt.Errorf("dependency: invalid type %q", r.Type)
	}

	// Reject obvious duplicates up front. The UNIQUE constraint also
	// catches it, but a clean error message beats a pg-formatted one.
	var count int64
	if err := s.pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM issue_relations
        WHERE source_id = $1 AND target_id = $2 AND type = $3`,
		r.SourceID, r.TargetID, string(r.Type),
	).Scan(&count); err != nil {
		return nil, fmt.Errorf("dependency: dupe check: %w", err)
	}
	if count > 0 {
		return nil, fmt.Errorf("dependency: relation already exists")
	}

	if err := s.assertIssuesShareWorkspace(ctx, r.SourceID, r.TargetID); err != nil {
		return nil, err
	}

	// Reject an edge that would close a directed blocking cycle (deadlock).
	cyclic, err := s.wouldCloseBlockingCycle(ctx, r.Type, r.SourceID, r.TargetID, r.WorkspaceID)
	if err != nil {
		return nil, err
	}
	if cyclic {
		return nil, ErrCycle
	}

	row, err := scanRelation(s.pool.QueryRow(ctx,
		`INSERT INTO issue_relations (source_id, target_id, type, workspace_id, created_by)
        VALUES ($1, $2, $3, $4, $5) RETURNING `+relationColumns,
		r.SourceID, r.TargetID, string(r.Type), r.WorkspaceID, r.CreatedBy,
	))
	if err != nil {
		return nil, fmt.Errorf("dependency: insert: %w", err)
	}

	if hasAutoInverse(r.Type) {
		// Idempotent — a previous half-completed create may have left
		// the inverse around. UNIQUE(source, target, type) backs the
		// ON CONFLICT.
		if _, err := s.pool.Exec(ctx,
			`INSERT INTO issue_relations (source_id, target_id, type, workspace_id, created_by)
            VALUES ($1, $2, $3, $4, $5)
            ON CONFLICT (source_id, target_id, type) DO NOTHING`,
			r.TargetID, r.SourceID, string(inverseType(r.Type)), r.WorkspaceID, r.CreatedBy,
		); err != nil {
			return nil, fmt.Errorf("dependency: insert inverse: %w", err)
		}
	}

	return row, nil
}

func scanRelation(s interface {
	Scan(...any) error
}) (*Relation, error) {
	var (
		r       Relation
		typeStr string
	)
	if err := s.Scan(&r.ID, &r.SourceID, &r.TargetID, &typeStr, &r.WorkspaceID, &r.CreatedBy, &r.CreatedAt); err != nil {
		return nil, err
	}
	r.Type = RelationType(typeStr)
	return &r, nil
}

// ─── Delete ─────────────────────────────────────────────────

// Delete removes the relation and, for symmetric types, its inverse.
// Both deletes are best-effort idempotent — deleting a relation that
// was already gone returns nil instead of an error.
func (s *Store) Delete(ctx context.Context, sourceID, targetID string, t RelationType) error {
	if s.pool == nil {
		return errors.New("dependency: store has no pool")
	}
	if _, err := s.pool.Exec(ctx,
		`DELETE FROM issue_relations WHERE source_id = $1 AND target_id = $2 AND type = $3`,
		sourceID, targetID, string(t),
	); err != nil {
		return fmt.Errorf("dependency: delete: %w", err)
	}
	if hasAutoInverse(t) {
		if _, err := s.pool.Exec(ctx,
			`DELETE FROM issue_relations WHERE source_id = $1 AND target_id = $2 AND type = $3`,
			targetID, sourceID, string(inverseType(t)),
		); err != nil {
			return fmt.Errorf("dependency: delete inverse: %w", err)
		}
	}
	return nil
}

// ─── GetRelations ───────────────────────────────────────────

// issueColumnsForRelations mirrors internal/issue.issueColumns but
// in this package's namespace. We keep it inline so the join query
// below isn't an opaque string of identifiers.
const issueColumnsForRelations = `id, workspace_id, team_id, project_id, number, identifier,
    title, description, status, priority,
    assignee_id, creator_id, cycle_id, parent_id,
    due_date, completed_at,
    lens_feature, ai_cost_usd, ai_tokens,
    labels, sort_order, created_at, updated_at`

// relationIssueCols qualifies every issueColumnsForRelations column with the "i."
// alias. The previous inline ReplaceAll only prefixed the first column on each
// line, leaving `workspace_id` unqualified — ambiguous against
// issue_relations.workspace_id on real Postgres (SQLSTATE 42702). The pgxmock store
// tests never exercised real SQL, so the crash hid until the integration harness.
var relationIssueCols = func() string {
	parts := strings.Split(issueColumnsForRelations, ",")
	for i, p := range parts {
		parts[i] = "i." + strings.TrimSpace(p)
	}
	return strings.Join(parts, ", ")
}()

// GetRelations returns all relations connected to issueID, presented
// from issueID's perspective. Outgoing rows (source=issueID) are
// returned as-is; incoming rows (target=issueID) are returned with
// source/target swapped and the type inverted.
//
// For symmetric types (blocks/blocked_by/duplicates) the inverse row
// already exists as an outgoing row, so we filter incoming duplicates
// out — otherwise the same logical relation would appear twice.
func (s *Store) GetRelations(ctx context.Context, issueID string) ([]RelationWithIssue, error) {
	if s.pool == nil {
		return nil, nil
	}

	// One query, two joins: the relation row, plus the *other* issue
	// (the one that isn't issueID).
	rows, err := s.pool.Query(ctx,
		`SELECT
            r.id, r.source_id, r.target_id, r.type, r.workspace_id, r.created_by, r.created_at,
            (r.source_id = $1) AS is_source,
            `+relationIssueCols+`
        FROM issue_relations r JOIN issues i ON i.id = CASE
            WHEN r.source_id = $1 THEN r.target_id
            ELSE r.source_id
        END
        -- Object-graph integrity: only surface a related issue that shares the
        -- caller issue's workspace, so a cross-workspace relation can't leak the
        -- foreign issue's content.
        AND i.workspace_id = (SELECT workspace_id FROM issues WHERE id = $1)
        WHERE r.source_id = $1 OR r.target_id = $1
        ORDER BY r.created_at ASC`,
		issueID,
	)
	if err != nil {
		return nil, fmt.Errorf("dependency: get relations: %w", err)
	}
	defer rows.Close()

	var out []RelationWithIssue
	for rows.Next() {
		var (
			rel      Relation
			typeStr  string
			isSource bool
			iss      model.Issue
			status   string
			priority int
		)
		if err := rows.Scan(
			&rel.ID, &rel.SourceID, &rel.TargetID, &typeStr, &rel.WorkspaceID, &rel.CreatedBy, &rel.CreatedAt,
			&isSource,
			&iss.ID, &iss.WorkspaceID, &iss.TeamID, &iss.ProjectID, &iss.Number, &iss.Identifier,
			&iss.Title, &iss.Description, &status, &priority,
			&iss.AssigneeID, &iss.CreatorID, &iss.CycleID, &iss.ParentID,
			&iss.DueDate, &iss.CompletedAt,
			&iss.LensFeature, &iss.AICostUSD, &iss.AITokens,
			&iss.Labels, &iss.SortOrder, &iss.CreatedAt, &iss.UpdatedAt,
		); err != nil {
			return nil, err
		}
		rel.Type = RelationType(typeStr)
		iss.Status = model.IssueStatus(status)
		iss.Priority = model.IssuePriority(priority)

		// Swap perspective for incoming rows. For symmetric types
		// (blocks/blocked_by/duplicates) the matching outgoing row is
		// already returned in this same query — skip the incoming
		// duplicate so callers don't see the same logical relation
		// twice.
		if !isSource {
			if hasAutoInverse(rel.Type) {
				continue
			}
			rel.SourceID, rel.TargetID = rel.TargetID, rel.SourceID
			rel.Type = inverseType(rel.Type)
		}

		out = append(out, RelationWithIssue{Relation: rel, Issue: iss})
	}
	return out, rows.Err()
}

// ─── GetBlockedBy ───────────────────────────────────────────

// GetBlockedBy returns the issues that currently block issueID and
// have not been completed (status not in done/cancelled). Powers the
// "Blocked by N issues" badge on the issue detail panel.
func (s *Store) GetBlockedBy(ctx context.Context, issueID string) ([]model.Issue, error) {
	if s.pool == nil {
		return nil, nil
	}
	rows, err := s.pool.Query(ctx,
		`SELECT `+relationIssueCols+`
        FROM issue_relations r JOIN issues i ON i.id = r.source_id
        WHERE r.target_id = $1 AND r.type = 'blocks'
          AND i.status NOT IN ('done', 'cancelled')
          AND i.workspace_id = (SELECT workspace_id FROM issues WHERE id = $1)`,
		issueID,
	)
	if err != nil {
		return nil, fmt.Errorf("dependency: blocked-by: %w", err)
	}
	defer rows.Close()

	var out []model.Issue
	for rows.Next() {
		var (
			iss      model.Issue
			status   string
			priority int
		)
		if err := rows.Scan(
			&iss.ID, &iss.WorkspaceID, &iss.TeamID, &iss.ProjectID, &iss.Number, &iss.Identifier,
			&iss.Title, &iss.Description, &status, &priority,
			&iss.AssigneeID, &iss.CreatorID, &iss.CycleID, &iss.ParentID,
			&iss.DueDate, &iss.CompletedAt,
			&iss.LensFeature, &iss.AICostUSD, &iss.AITokens,
			&iss.Labels, &iss.SortOrder, &iss.CreatedAt, &iss.UpdatedAt,
		); err != nil {
			return nil, err
		}
		iss.Status = model.IssueStatus(status)
		iss.Priority = model.IssuePriority(priority)
		out = append(out, iss)
	}
	return out, rows.Err()
}

// ─── IsBlocked ──────────────────────────────────────────────

// IsBlocked is a fast COUNT-based variant of GetBlockedBy. Used by
// issue.Store.GetByID to populate Issue.IsBlocked without a per-issue
// roundtrip in the list path (list reads omit it).
//
// Cycles (A blocks B blocks A) collapse to "both blocked" — each
// issue counts the other as an open blocker. That matches user
// intuition: if you're in a cycle, you're stuck.
func (s *Store) IsBlocked(ctx context.Context, issueID string) (bool, error) {
	if s.pool == nil {
		return false, nil
	}
	var count int64
	err := s.pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM issue_relations r JOIN issues i ON i.id = r.source_id
        WHERE r.target_id = $1 AND r.type = 'blocks'
          AND i.status NOT IN ('done', 'cancelled')
          AND i.workspace_id = (SELECT workspace_id FROM issues WHERE id = $1)`,
		issueID,
	).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("dependency: is-blocked: %w", err)
	}
	return count > 0, nil
}

// ─── GetDependencyGraph ─────────────────────────────────────

// GetDependencyGraph does BFS from rootIssueID up to `depth` hops
// (capped at MaxGraphDepth). One query per BFS layer expands every
// frontier issue at once; node metadata is fetched in one bulk
// SELECT at the end. The result is plain nodes + edges so the
// frontend can lay them out however it likes.
func (s *Store) GetDependencyGraph(ctx context.Context, workspaceID, rootIssueID string, depth int) (*DependencyGraph, error) {
	if s.pool == nil {
		return &DependencyGraph{Nodes: []GraphNode{}, Edges: []GraphEdge{}}, nil
	}
	if depth < 0 {
		depth = 0
	}
	if depth > MaxGraphDepth {
		depth = MaxGraphDepth
	}

	// Start with the root.
	var root GraphNode
	err := s.pool.QueryRow(ctx,
		`SELECT id, identifier, title, status, workspace_id FROM issues WHERE id = $1 AND workspace_id = $2`,
		rootIssueID, workspaceID,
	).Scan(&root.ID, &root.Identifier, &root.Title, &root.Status, new(string))
	if err != nil {
		return nil, fmt.Errorf("dependency: graph root: %w", err)
	}

	visited := map[string]bool{root.ID: true}
	frontier := []string{root.ID}
	edges := []GraphEdge{}
	// discoveredIDs tracks every issue ID seen through edge expansion.
	// The root is excluded because we already have its metadata from
	// the initial lookup — keeping it out of the bulk query keeps the
	// IN list minimal and the test deterministic.
	discoveredIDs := map[string]bool{}

	for d := 0; d < depth && len(frontier) > 0; d++ {
		rows, err := s.pool.Query(ctx,
			`SELECT source_id, target_id, type FROM issue_relations
            WHERE workspace_id = $2 AND (source_id = ANY($1) OR target_id = ANY($1))`,
			frontier, workspaceID,
		)
		if err != nil {
			return nil, fmt.Errorf("dependency: graph expand: %w", err)
		}
		next := []string{}
		for rows.Next() {
			var src, tgt, typ string
			if err := rows.Scan(&src, &tgt, &typ); err != nil {
				rows.Close()
				return nil, err
			}
			edges = append(edges, GraphEdge{Source: src, Target: tgt, Type: RelationType(typ)})
			for _, id := range []string{src, tgt} {
				if id != root.ID {
					discoveredIDs[id] = true
				}
				if !visited[id] {
					visited[id] = true
					next = append(next, id)
				}
			}
		}
		rows.Close()
		frontier = next
	}

	// Start the node list with the root we already loaded, then
	// bulk-fetch metadata for every other discovered ID.
	nodes := []GraphNode{root}
	if len(discoveredIDs) > 0 {
		nodeIDs := make([]string, 0, len(discoveredIDs))
		for id := range discoveredIDs {
			nodeIDs = append(nodeIDs, id)
		}
		// Deterministic order so the query plan + tests stay stable.
		sort.Strings(nodeIDs)
		rows, err := s.pool.Query(ctx,
			// Scope the node-metadata fetch to the queried workspace so a foreign
			// issue reached via a raw/legacy cross-workspace relation can't surface
			// its content here.
			`SELECT id, identifier, title, status FROM issues WHERE id = ANY($1) AND workspace_id = $2`,
			nodeIDs, workspaceID,
		)
		if err != nil {
			return nil, fmt.Errorf("dependency: graph nodes: %w", err)
		}
		for rows.Next() {
			var n GraphNode
			if err := rows.Scan(&n.ID, &n.Identifier, &n.Title, &n.Status); err != nil {
				rows.Close()
				return nil, err
			}
			nodes = append(nodes, n)
		}
		rows.Close()
	}

	// De-duplicate edges (one logical relation may appear twice when
	// both directions are stored — keep the deterministic "outgoing"
	// row).
	edges = dedupeEdges(edges)

	return &DependencyGraph{Nodes: nodes, Edges: edges}, nil
}

func dedupeEdges(in []GraphEdge) []GraphEdge {
	seen := map[string]bool{}
	out := make([]GraphEdge, 0, len(in))
	for _, e := range in {
		// blocks/blocked_by collapse: skip the blocked_by half so the
		// rendered arrow always points blocker→blocked.
		if e.Type == RelationBlockedBy {
			continue
		}
		key := e.Source + "→" + e.Target + ":" + string(e.Type)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, e)
	}
	return out
}

// ─── GetRelationStats ──────────────────────────────────────

// RelationStats is the dashboard-style rollup the workspace settings
// + sprint planning views read. One query, four aggregates.
type RelationStats struct {
	TotalRelations int `json:"total_relations"`
	BlockingChains int `json:"blocking_chains"`
	BlockedIssues  int `json:"blocked_issues"`
	DuplicatePairs int `json:"duplicate_pairs"`
}

// GetRelationStats counts the relation rows once and projects four
// metrics out of the same CTE: total relations, the number of
// "blocking chains" (sources that block ≥ 2 distinct targets — they
// drive the sprint-planner UI's "biggest blockers" highlight), how
// many issues are currently the target of a "blocks" relation, and
// the number of duplicate pairs (rows are stored in both directions,
// so /2 to get pair count).
func (s *Store) GetRelationStats(ctx context.Context, workspaceID string) (*RelationStats, error) {
	if s.pool == nil {
		return &RelationStats{}, nil
	}
	var total, chains, blocked, dupes int64
	err := s.pool.QueryRow(ctx,
		`WITH r AS (SELECT * FROM issue_relations WHERE workspace_id = $1)
        SELECT
            (SELECT COUNT(*) FROM r)                                            AS total_relations,
            (SELECT COUNT(*) FROM (
                SELECT source_id FROM r WHERE type = 'blocks'
                GROUP BY source_id HAVING COUNT(DISTINCT target_id) >= 2) c)    AS blocking_chains,
            (SELECT COUNT(DISTINCT target_id) FROM r WHERE type = 'blocks')     AS blocked_issues,
            (SELECT COUNT(*) / 2 FROM r WHERE type = 'duplicates')              AS duplicate_pairs`,
		workspaceID,
	).Scan(&total, &chains, &blocked, &dupes)
	if err != nil {
		return nil, fmt.Errorf("dependency: stats: %w", err)
	}
	return &RelationStats{
		TotalRelations: int(total),
		BlockingChains: int(chains),
		BlockedIssues:  int(blocked),
		DuplicatePairs: int(dupes),
	}, nil
}

// ─── GetBlockingIssues ─────────────────────────────────────

// BlockingIssue is one row in the sprint-planner "biggest blockers"
// list. BlocksCount + BlockedIssues are derived per-issue and only
// produced for source-of-blocks rows.
type BlockingIssue struct {
	model.Issue
	BlocksCount   int      `json:"blocks_count"`
	BlockedIssues []string `json:"blocked_issue_ids"`
}

// GetBlockingIssues returns issues that block at least one other
// issue, sorted blocks-count-DESC. cycleID narrows to a specific
// cycle so sprint planning can highlight "what's slowing this
// sprint". The aggregated target IDs travel back as a TEXT[] which
// pgx scans straight into []string.
func (s *Store) GetBlockingIssues(ctx context.Context, workspaceID string, cycleID *string) ([]BlockingIssue, error) {
	if s.pool == nil {
		return nil, nil
	}
	args := []any{workspaceID}
	cycleClause := ""
	if cycleID != nil {
		cycleClause = " AND i.cycle_id = $2"
		args = append(args, *cycleID)
	}
	sql := `SELECT i.id, i.workspace_id, i.team_id, i.project_id, i.number, i.identifier,
                i.title, i.description, i.status, i.priority,
                i.assignee_id, i.creator_id, i.cycle_id, i.parent_id,
                i.due_date, i.completed_at,
                i.lens_feature, i.ai_cost_usd, i.ai_tokens,
                i.labels, i.sort_order, i.created_at, i.updated_at,
                COUNT(r.target_id)             AS blocks_count,
                array_agg(r.target_id)         AS blocked_ids
            FROM issues i
            JOIN issue_relations r ON r.source_id = i.id AND r.type = 'blocks'
            WHERE r.workspace_id = $1` + cycleClause + `
            GROUP BY i.id
            ORDER BY blocks_count DESC, i.created_at ASC`
	rows, err := s.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("dependency: blocking: %w", err)
	}
	defer rows.Close()
	var out []BlockingIssue
	for rows.Next() {
		var (
			bi       BlockingIssue
			status   string
			priority int
			count    int64
		)
		if err := rows.Scan(
			&bi.ID, &bi.WorkspaceID, &bi.TeamID, &bi.ProjectID, &bi.Number, &bi.Identifier,
			&bi.Title, &bi.Description, &status, &priority,
			&bi.AssigneeID, &bi.CreatorID, &bi.CycleID, &bi.ParentID,
			&bi.DueDate, &bi.CompletedAt,
			&bi.LensFeature, &bi.AICostUSD, &bi.AITokens,
			&bi.Labels, &bi.SortOrder, &bi.CreatedAt, &bi.UpdatedAt,
			&count, &bi.BlockedIssues,
		); err != nil {
			return nil, err
		}
		bi.Status = model.IssueStatus(status)
		bi.Priority = model.IssuePriority(priority)
		bi.BlocksCount = int(count)
		out = append(out, bi)
	}
	return out, rows.Err()
}

// ─── BulkCreateRelations ───────────────────────────────────

// MaxBulkRelationTargets caps the per-request fan-out so a single
// malicious POST can't hot-loop the database. Matches the frontend
// "Link multiple" picker's hard limit.
const MaxBulkRelationTargets = 50

// BulkCreateRelations inserts one source→target relation per entry
// in `targets`. Uses UNNEST + ON CONFLICT DO NOTHING so duplicates
// silently skip — the returned count reflects rows actually written.
//
// The auto-inverse logic that Create runs is deliberately NOT
// applied here. The bulk path is for relates_to / clones — adding
// 50 blocks at once would create 100 rows and is the wrong UX. Use
// per-row Create for blocks/duplicates.
func (s *Store) BulkCreateRelations(ctx context.Context, rel Relation, targets []string) (int, error) {
	if s.pool == nil {
		return 0, errors.New("dependency: store has no pool")
	}
	if rel.SourceID == "" {
		return 0, errors.New("dependency: source_id required")
	}
	if _, ok := validTypes[rel.Type]; !ok {
		return 0, fmt.Errorf("dependency: invalid type %q", rel.Type)
	}
	if len(targets) == 0 {
		return 0, nil
	}
	if len(targets) > MaxBulkRelationTargets {
		return 0, fmt.Errorf("dependency: max %d targets per bulk request", MaxBulkRelationTargets)
	}
	for _, t := range targets {
		if t == rel.SourceID {
			return 0, errors.New("dependency: targets list contains source — self-relations rejected")
		}
	}

	// Object-graph integrity: every target must share the source issue's workspace.
	// A missing source (NULL subquery) or any cross-workspace/absent target counts
	// as "bad" and refuses the whole batch.
	var badTargets int64
	if err := s.pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM UNNEST($1::text[]) AS t
        WHERE NOT EXISTS (
            SELECT 1 FROM issues i
            WHERE i.id = t
              AND i.workspace_id = (SELECT workspace_id FROM issues WHERE id = $2))`,
		targets, rel.SourceID,
	).Scan(&badTargets); err != nil {
		return 0, fmt.Errorf("dependency: bulk workspace check: %w", err)
	}
	if badTargets > 0 {
		return 0, errors.New("dependency: one or more targets are in a different workspace or do not exist")
	}

	// Cycle detection per target: a blocks-family bulk insert must not close a
	// directed cycle for any of its edges.
	for _, t := range targets {
		cyclic, err := s.wouldCloseBlockingCycle(ctx, rel.Type, rel.SourceID, t, rel.WorkspaceID)
		if err != nil {
			return 0, err
		}
		if cyclic {
			return 0, fmt.Errorf("%w (target %s)", ErrCycle, t)
		}
	}

	tag, err := s.pool.Exec(ctx,
		`INSERT INTO issue_relations (source_id, target_id, workspace_id, type, created_by)
        SELECT $1, t, $3, $4, $5
        FROM UNNEST($2::text[]) AS t
        ON CONFLICT (source_id, target_id, type) DO NOTHING`,
		rel.SourceID, targets, rel.WorkspaceID, string(rel.Type), rel.CreatedBy,
	)
	if err != nil {
		return 0, fmt.Errorf("dependency: bulk insert: %w", err)
	}
	return int(tag.RowsAffected()), nil
}
