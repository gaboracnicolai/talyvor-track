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
            i.`+strings.ReplaceAll(issueColumnsForRelations, ",\n    ", ", i.")+`
        FROM issue_relations r JOIN issues i ON i.id = CASE
            WHEN r.source_id = $1 THEN r.target_id
            ELSE r.source_id
        END
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
		`SELECT `+issueColumnsForRelations+`
        FROM issue_relations r JOIN issues i ON i.id = r.source_id
        WHERE r.target_id = $1 AND r.type = 'blocks'
          AND i.status NOT IN ('done', 'cancelled')`,
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
          AND i.status NOT IN ('done', 'cancelled')`,
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
		`SELECT id, identifier, title, status, workspace_id FROM issues WHERE id = $1`,
		rootIssueID,
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
			`SELECT id, identifier, title, status FROM issues WHERE id = ANY($1)`,
			nodeIDs,
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
