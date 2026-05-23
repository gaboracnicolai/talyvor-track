// Command track is the Talyvor Track API server.
//
// Starts a chi-router HTTP server on :3000 (configurable via
// TRACK_LISTEN_ADDR) that serves the issue / project / team /
// workspace REST API plus Prometheus metrics. The server is
// shutdown-safe — a SIGTERM triggers graceful drain of in-flight
// requests before closing the database pool.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/talyvor/track/internal/ai"
	"github.com/talyvor/track/internal/analytics"
	"github.com/talyvor/track/internal/automation"
	"github.com/talyvor/track/internal/config"
	"github.com/talyvor/track/internal/customfield"
	"github.com/talyvor/track/internal/cycle"
	"github.com/talyvor/track/internal/db"
	"github.com/talyvor/track/internal/importer"
	"github.com/talyvor/track/internal/issue"
	"github.com/talyvor/track/internal/label"
	"github.com/talyvor/track/internal/lensintegration"
	"github.com/talyvor/track/internal/mcp"
	"github.com/talyvor/track/internal/metrics"
	"github.com/talyvor/track/internal/milestone"
	"github.com/talyvor/track/internal/model"
	"github.com/talyvor/track/internal/notification"
	"github.com/talyvor/track/internal/project"
	"github.com/talyvor/track/internal/realtime"
	"github.com/talyvor/track/internal/team"
	"github.com/talyvor/track/internal/workflow"
	"github.com/talyvor/track/internal/workspace"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	slog.SetDefault(logger)

	cfg, err := config.Load()
	if err != nil {
		slog.Error("config load failed", slog.String("err", err.Error()))
		os.Exit(1)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	pool, err := db.New(ctx, cfg.DatabaseURL)
	if err != nil {
		slog.Error("db init failed", slog.String("err", err.Error()))
		os.Exit(1)
	}
	defer pool.Close()

	// Stores own the SQL; handlers own the JSON; main wires them.
	hub := realtime.NewHub()
	go hub.Run(ctx)
	notifier := realtime.NewNotifier(hub)

	workflowEngine := workflow.New(pool)
	workspaceStore := workspace.NewStore(pool)
	customFieldStore := customfield.NewStore(pool)
	// issueStore reads custom-field values when serving issues so the
	// REST + MCP responses always include the FieldValues map without
	// callers having to stitch the data.
	issueStore := issue.NewStore(pool).WithFieldFetcher(customFieldStore)
	projectStore := project.NewStore(pool)
	cycleStore := cycle.NewStore(pool)
	notificationStore := notification.NewStore(pool)

	wsHandler := workspace.NewHandler(workspaceStore)
	teamHandler := team.NewHandler(team.NewStore(pool)).WithSeeder(workflowEngine)
	projectHandler := project.NewHandler(projectStore)
	issueHandler := issue.NewHandler(issueStore).WithNotifier(notifier).WithCustomFields(customFieldStore)
	customFieldHandler := customfield.NewHandler(customFieldStore)
	workflowHandler := workflow.NewHandler(workflowEngine)
	labelHandler := label.NewHandler(label.NewStore(pool))
	cycleHandler := cycle.NewHandler(cycleStore)
	milestoneHandler := milestone.NewHandler(milestone.NewStore(pool))
	notificationHandler := notification.NewHandler(notificationStore)

	// Lens integration: read side (client + AI cost endpoints), sync
	// loop (15-minute interval), and webhook receiver. Lens config is
	// optional — empty URL keeps every endpoint reachable but Lens
	// data is just absent.
	lensClient := lensintegration.New(cfg.LensURL, cfg.LensAPIKey)
	lensHandler := lensintegration.NewHandler(lensClient, issueStore)
	lensWebhook := lensintegration.NewWebhookHandler(cfg.LensWebhookSecret, issueStore, notificationStore, notifier)
	if lensClient.IsConfigured() {
		lensSyncer := lensintegration.NewSyncer(lensClient, issueStore, workspaceStore)
		go lensSyncer.StartSync(ctx, 15*time.Minute)
	}

	// AI engine: every Track AI feature routes through Lens via this
	// engine. issueStore doubles as the full-text fallback for
	// semantic search; pool is needed for the issue_embeddings table.
	aiEngine := ai.New(lensClient, issueStore, pool)
	aiHandler := ai.NewHandler(aiEngine, issueStore)

	// Automation engine. Slack notifier is the only side-channel —
	// every other action mutates issues through issueStore directly.
	slackNotifier := automation.NewSlackNotifier()
	automationEngine := automation.New(pool, issueStore, slackNotifier)
	automationHandler := automation.NewHandler(automationEngine)
	githubHandler := automation.NewGitHubHandler(automationEngine, issueStore, os.Getenv("TRACK_GITHUB_WEBHOOK_SECRET"))

	// Adapter bridges the issue handler's string-typed automation
	// interface to the engine's typed RuleTrigger.
	issueHandler.WithAutomation(automationAdapter{engine: automationEngine})

	// Analytics engine: pure-read reports over the same tables every
	// other store touches. No mutations, no state of its own.
	analyticsEngine := analytics.New(pool)
	analyticsHandler := analytics.NewHandler(analyticsEngine)

	// MCP server: JSON-RPC + SSE surface for agent integrations. Mounts
	// on the public router (no auth, no /v1 prefix) so MCP clients can
	// find /mcp at a stable path. The get_ai_costs tool is unique to
	// Track — no other tracker exposes LLM spend through MCP.
	mcpServer := mcp.New(
		issueStore, projectStore, cycleStore,
		aiEngine, analyticsEngine, "0.1.0",
	).WithMembersPool(pool)

	// CSV importer: lets new customers migrate from Linear or Jira in
	// one curl call. Mounted under /v1/import/* so it inherits the
	// auth surface every other /v1 endpoint sits behind.
	importerHandler := importer.NewHandler(importer.New(issueStore))

	// Preload rules for every workspace at startup so the first
	// matching event doesn't pay for an on-demand DB read.
	if ids, err := workspaceStore.ListIDs(ctx); err == nil {
		for _, ws := range ids {
			_ = automationEngine.LoadRules(ctx, ws)
		}
	}

	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(middleware.Recoverer)
	r.Use(middleware.Timeout(30 * time.Second))
	r.Use(metricsMiddleware)

	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	})
	r.Handle("/metrics", metrics.Handler())

	// MCP endpoints. Mounted on the public router so they bypass any
	// future auth middleware — MCP clients authenticate via the
	// Track-issued workspace API key embedded in tool arguments, not
	// HTTP headers.
	r.Post("/mcp", mcpServer.HandleRPC)
	r.Get("/mcp/sse", mcpServer.HandleSSE)

	r.Route("/v1", func(r chi.Router) {
		wsHandler.Mount(r)
		teamHandler.Mount(r)
		projectHandler.Mount(r)
		issueHandler.Mount(r)
		workflowHandler.Mount(r)
		labelHandler.Mount(r)
		cycleHandler.Mount(r)
		milestoneHandler.Mount(r)
		notificationHandler.Mount(r)
		lensHandler.Mount(r)
		aiHandler.Mount(r)
		automationHandler.Mount(r)
		analyticsHandler.Mount(r)
		importerHandler.Mount(r)
		customFieldHandler.Mount(r)

		// Inbound webhook from Lens. Validated via HMAC-SHA256 of the
		// request body with the shared secret — see
		// internal/lensintegration/webhook.go for the verification
		// path.
		r.Post("/lens/webhook", lensWebhook.ServeHTTP)

		// GitHub webhook: PR merged → close referenced issues,
		// PR opened → leave a tracking comment. HMAC validated via
		// X-Hub-Signature-256.
		r.Post("/webhooks/github", githubHandler.ServeHTTP)

		// WebSocket upgrade — clients connect here and receive live
		// events for the issues / comments / cycles they subscribe to.
		r.Get("/ws", hub.ServeWS)
	})

	srv := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           r,
		ReadHeaderTimeout: 10 * time.Second,
	}

	serverErr := make(chan error, 1)
	go func() {
		slog.Info("track server listening", slog.String("addr", cfg.ListenAddr))
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErr <- err
		}
		close(serverErr)
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)

	select {
	case <-stop:
		slog.Info("shutdown signal received")
	case err := <-serverErr:
		slog.Error("server error", slog.String("err", err.Error()))
		os.Exit(1)
	}

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer shutdownCancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		slog.Error("shutdown error", slog.String("err", err.Error()))
	}
}

// metricsMiddleware records request count + latency without holding any
// per-request state on the handler. Cardinality is bounded by chi's
// RoutePattern() helper — never raw URL paths.
func metricsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
		next.ServeHTTP(ww, r)
		path := chi.RouteContext(r.Context()).RoutePattern()
		if path == "" {
			path = "unknown"
		}
		metrics.APIRequests.WithLabelValues(r.Method, path, strconv.Itoa(ww.Status())).Inc()
		metrics.APILatency.WithLabelValues(r.Method, path).Observe(time.Since(start).Seconds())
	})
}

// automationAdapter bridges issue.Handler's string-typed
// automationFirer interface to automation.Engine.Fire's typed
// RuleTrigger. The conversion is a no-op at runtime — RuleTrigger
// is itself a string — but the type wrapper keeps each package's
// public surface free of cross-imports.
type automationAdapter struct {
	engine *automation.Engine
}

func (a automationAdapter) Fire(ctx context.Context, trigger string, workspaceID string, issue model.Issue, changes map[string]any) error {
	return a.engine.Fire(ctx, automation.RuleTrigger(trigger), workspaceID, issue, changes)
}
