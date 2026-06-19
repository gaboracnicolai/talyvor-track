package featureboard

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"github.com/talyvor/track/internal/model"
)

// issueCreator is the subset of issue.Store the "convert to issue"
// admin action calls. Local interface so featureboard never imports
// the issue package directly.
type issueCreator interface {
	Create(ctx context.Context, i model.Issue) (*model.Issue, error)
}

type Handler struct {
	store  *Store
	issues issueCreator
}

func NewHandler(store *Store, issues issueCreator) *Handler {
	return &Handler{store: store, issues: issues}
}

// Mount wires the two route trees:
//   - /v1/public/boards/... is anonymous (no auth middleware in
//     front of these handlers).
//   - /v1/workspaces/{wsID}/boards/... is the admin tree, mounted
//     inside the auth-bearing /v1 group along with every other
//     workspace-scoped surface.
func (h *Handler) Mount(r chi.Router) {
	// Admin tree.
	r.Route("/workspaces/{wsID}/boards", func(r chi.Router) {
		r.Get("/", h.AdminList)
		r.Post("/", h.AdminCreate)
		r.Get("/{boardID}/stats", h.AdminStats)
		r.Patch("/{boardID}/posts/{postID}", h.AdminUpdatePost)
		r.Post("/{boardID}/posts/{postID}/convert", h.AdminConvert)
	})

	// Public tree.
	r.Route("/public/boards/{wsSlug}/{boardSlug}", func(r chi.Router) {
		r.Get("/", h.PublicBoard)
		r.Get("/posts", h.PublicListPosts)
		r.Post("/posts", h.PublicCreatePost)
		r.Post("/posts/{postID}/vote", h.PublicVote)
		r.Delete("/posts/{postID}/vote", h.PublicUnvote)
	})
}

type apiError struct {
	Error string `json:"error"`
	Code  string `json:"code"`
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
func writeErr(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, apiError{Error: msg, Code: code})
}

// ─── admin handlers ─────────────────────────────────────────

func (h *Handler) AdminList(w http.ResponseWriter, r *http.Request) {
	out, err := h.store.ListBoards(r.Context(), chi.URLParam(r, "wsID"))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "LIST_FAILED", err.Error())
		return
	}
	if out == nil {
		out = []Board{}
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *Handler) AdminCreate(w http.ResponseWriter, r *http.Request) {
	wsID := chi.URLParam(r, "wsID")
	var in Board
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, "BAD_JSON", err.Error())
		return
	}
	in.WorkspaceID = wsID
	out, err := h.store.CreateBoard(r.Context(), in)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "CREATE_FAILED", err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, out)
}

func (h *Handler) AdminStats(w http.ResponseWriter, r *http.Request) {
	out, err := h.store.GetStats(r.Context(), chi.URLParam(r, "boardID"))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "STATS_FAILED", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *Handler) AdminUpdatePost(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Status  PostStatus `json:"status"`
		IssueID *string    `json:"issue_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, "BAD_JSON", err.Error())
		return
	}
	if err := h.store.UpdateStatus(r.Context(), chi.URLParam(r, "wsID"), chi.URLParam(r, "boardID"), chi.URLParam(r, "postID"), in.Status, in.IssueID); err != nil {
		writeErr(w, http.StatusBadRequest, "UPDATE_FAILED", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// AdminConvert turns a feature post into a Track issue and links the
// two via feature_posts.issue_id. The team to associate the issue
// with comes from the request body — admins pick it.
func (h *Handler) AdminConvert(w http.ResponseWriter, r *http.Request) {
	wsID := chi.URLParam(r, "wsID")
	postID := chi.URLParam(r, "postID")
	var in struct {
		TeamID    string `json:"team_id"`
		CreatorID string `json:"creator_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, "BAD_JSON", err.Error())
		return
	}
	if in.TeamID == "" {
		writeErr(w, http.StatusBadRequest, "BAD_PARAMS", "team_id required")
		return
	}
	post, err := h.store.GetPost(r.Context(), postID)
	if err != nil {
		writeErr(w, http.StatusNotFound, "POST_NOT_FOUND", err.Error())
		return
	}
	// Object-graph integrity: only convert a post that belongs to this workspace —
	// don't copy a cross-workspace post's content into a new issue here.
	if post.WorkspaceID != wsID {
		writeErr(w, http.StatusNotFound, "POST_NOT_FOUND", "post not found in workspace")
		return
	}
	if h.issues == nil {
		writeErr(w, http.StatusInternalServerError, "ISSUES_UNAVAILABLE",
			"issue store not wired")
		return
	}
	created, err := h.issues.Create(r.Context(), model.Issue{
		WorkspaceID: wsID,
		TeamID:      in.TeamID,
		Title:       post.Title,
		Description: post.Description + "\n\n(Converted from feature board post)",
		CreatorID:   in.CreatorID,
		Status:      model.StatusBacklog,
	})
	if err != nil {
		writeErr(w, http.StatusBadRequest, "CONVERT_FAILED", err.Error())
		return
	}
	// Link the post back to the new issue and bump status to planned
	// so the public board reflects "we're going to build it".
	if err := h.store.UpdateStatus(r.Context(), wsID, chi.URLParam(r, "boardID"), postID, PostStatusPlanned, &created.ID); err != nil {
		writeErr(w, http.StatusInternalServerError, "LINK_FAILED", err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"issue_id":   created.ID,
		"identifier": created.Identifier,
		"post_id":    postID,
	})
}

// ─── public handlers ────────────────────────────────────────

// PublicBoard returns the board + stats. The board's existence is
// public information; the 404 path keeps a private workspace from
// leaking the existence of internal boards.
func (h *Handler) PublicBoard(w http.ResponseWriter, r *http.Request) {
	wsSlug := chi.URLParam(r, "wsSlug")
	boardSlug := chi.URLParam(r, "boardSlug")
	board, err := h.store.GetPublicBoard(r.Context(), wsSlug, boardSlug)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeErr(w, http.StatusNotFound, "NOT_FOUND", "board not found")
			return
		}
		writeErr(w, http.StatusInternalServerError, "READ_FAILED", err.Error())
		return
	}
	stats, _ := h.store.GetStats(r.Context(), board.ID)
	writeJSON(w, http.StatusOK, map[string]any{
		"board": board,
		"stats": stats,
	})
}

func (h *Handler) PublicListPosts(w http.ResponseWriter, r *http.Request) {
	board, err := h.store.GetPublicBoard(r.Context(),
		chi.URLParam(r, "wsSlug"), chi.URLParam(r, "boardSlug"))
	if err != nil {
		writeErr(w, http.StatusNotFound, "BOARD_NOT_FOUND", err.Error())
		return
	}
	var status *PostStatus
	if v := r.URL.Query().Get("status"); v != "" {
		s := PostStatus(v)
		status = &s
	}
	posts, err := h.store.ListPosts(r.Context(), board.ID, status, r.URL.Query().Get("order_by"))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "LIST_FAILED", err.Error())
		return
	}
	if posts == nil {
		posts = []FeaturePost{}
	}
	// If the caller passes their email, decorate each post with a
	// has_voted flag so the UI can render the toggled vote button
	// without a follow-up roundtrip.
	voter := r.Header.Get("X-Voter-Email")
	type decorated struct {
		FeaturePost
		HasVoted bool `json:"has_voted"`
	}
	out := make([]decorated, 0, len(posts))
	for _, p := range posts {
		dec := decorated{FeaturePost: p}
		if voter != "" {
			if voted, err := h.store.HasVoted(r.Context(), p.ID, voter); err == nil {
				dec.HasVoted = voted
			}
		}
		out = append(out, dec)
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *Handler) PublicCreatePost(w http.ResponseWriter, r *http.Request) {
	board, err := h.store.GetPublicBoard(r.Context(),
		chi.URLParam(r, "wsSlug"), chi.URLParam(r, "boardSlug"))
	if err != nil {
		writeErr(w, http.StatusNotFound, "BOARD_NOT_FOUND", err.Error())
		return
	}
	var in struct {
		Title       string `json:"title"`
		Description string `json:"description"`
		AuthorName  string `json:"author_name"`
		AuthorEmail string `json:"author_email"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, "BAD_JSON", err.Error())
		return
	}
	if !board.AllowAnonymous && in.AuthorEmail == "" {
		writeErr(w, http.StatusBadRequest, "EMAIL_REQUIRED",
			"this board requires a verified email to post")
		return
	}
	post, err := h.store.CreatePost(r.Context(), FeaturePost{
		WorkspaceID: board.WorkspaceID,
		BoardID:     board.ID,
		Title:       in.Title,
		Description: in.Description,
		AuthorName:  in.AuthorName,
		AuthorEmail: in.AuthorEmail,
	})
	if err != nil {
		// Rate-limit / validation errors come back as 429 / 400 with
		// the store's own message so the UI can surface it directly.
		writeErr(w, http.StatusBadRequest, "POST_FAILED", err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, post)
}

func (h *Handler) PublicVote(w http.ResponseWriter, r *http.Request) {
	postID := chi.URLParam(r, "postID")
	var in struct {
		Email string `json:"email"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, "BAD_JSON", err.Error())
		return
	}
	count, err := h.store.Vote(r.Context(), chi.URLParam(r, "wsSlug"), chi.URLParam(r, "boardSlug"), postID, in.Email)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "VOTE_FAILED", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"vote_count": count,
		"voted":      true,
	})
}

func (h *Handler) PublicUnvote(w http.ResponseWriter, r *http.Request) {
	postID := chi.URLParam(r, "postID")
	var in struct {
		Email string `json:"email"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, "BAD_JSON", err.Error())
		return
	}
	count, err := h.store.Unvote(r.Context(), chi.URLParam(r, "wsSlug"), chi.URLParam(r, "boardSlug"), postID, in.Email)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "UNVOTE_FAILED", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"vote_count": count,
		"voted":      false,
	})
}
