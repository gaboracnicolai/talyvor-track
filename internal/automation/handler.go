package automation

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/talyvor/track/internal/authz"
	"github.com/talyvor/track/internal/httpx"
)

type Handler struct{ engine *Engine }

func NewHandler(engine *Engine) *Handler { return &Handler{engine: engine} }

func (h *Handler) Mount(r chi.Router) {
	r.Route("/workspaces/{wsID}/automation", func(r chi.Router) {
		r.Get("/rules", h.ListRules)
		r.Post("/rules", h.CreateRule)
		r.Delete("/rules/{id}", h.DeleteRule)
		r.Get("/logs", h.ListLogs)
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

func (h *Handler) CreateRule(w http.ResponseWriter, r *http.Request) {
	wsID, ok := authz.WorkspaceID(r.Context())
	if !ok {
		writeJSON(w, http.StatusForbidden, apiError{Error: "not a member of this workspace", Code: "WORKSPACE_FORBIDDEN"})
		return
	}
	var in Rule
	if !httpx.DecodeJSON(w, r, &in) {
		return
	}
	in.WorkspaceID = wsID
	out, err := h.engine.AddRule(r.Context(), in)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "CREATE_FAILED", err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, out)
}

func (h *Handler) ListRules(w http.ResponseWriter, r *http.Request) {
	wsID, ok := authz.WorkspaceID(r.Context())
	if !ok {
		writeJSON(w, http.StatusForbidden, apiError{Error: "not a member of this workspace", Code: "WORKSPACE_FORBIDDEN"})
		return
	}
	rules := h.engine.ListRules(wsID)
	if rules == nil {
		rules = []Rule{}
	}
	writeJSON(w, http.StatusOK, rules)
}

func (h *Handler) DeleteRule(w http.ResponseWriter, r *http.Request) {
	if err := h.engine.DeleteRule(r.Context(), chi.URLParam(r, "id")); err != nil {
		writeErr(w, http.StatusInternalServerError, "DELETE_FAILED", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// ListLogs returns the most recent automation_logs rows for the
// workspace. The query joins logs to rules so we can filter by
// workspace; logs themselves only carry rule_id.
func (h *Handler) ListLogs(w http.ResponseWriter, r *http.Request) {
	wsID, ok := authz.WorkspaceID(r.Context())
	if !ok {
		writeJSON(w, http.StatusForbidden, apiError{Error: "not a member of this workspace", Code: "WORKSPACE_FORBIDDEN"})
		return
	}
	if h.engine.pool == nil {
		writeJSON(w, http.StatusOK, []map[string]any{})
		return
	}
	rows, err := h.engine.pool.Query(r.Context(),
		`SELECT l.id, l.rule_id, l.issue_id, l.trigger, l.actions_taken,
            l.success, l.error, l.created_at
        FROM automation_logs l
        JOIN automation_rules ar ON ar.id = l.rule_id
        WHERE ar.workspace_id = $1
        ORDER BY l.created_at DESC LIMIT 100`,
		wsID,
	)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "LIST_FAILED", err.Error())
		return
	}
	defer rows.Close()
	var out []map[string]any
	for rows.Next() {
		var (
			id, ruleID, trig, errStr string
			issueID                  *string
			actions                  []string
			success                  bool
			createdAt                interface{}
		)
		if err := rows.Scan(&id, &ruleID, &issueID, &trig, &actions, &success, &errStr, &createdAt); err != nil {
			continue
		}
		out = append(out, map[string]any{
			"id":            id,
			"rule_id":       ruleID,
			"issue_id":      issueID,
			"trigger":       trig,
			"actions_taken": actions,
			"success":       success,
			"error":         errStr,
			"created_at":    createdAt,
		})
	}
	if out == nil {
		out = []map[string]any{}
	}
	writeJSON(w, http.StatusOK, out)
}
