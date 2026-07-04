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
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/redis/go-redis/v9"

	"github.com/talyvor/track/internal/ai"
	"github.com/talyvor/track/internal/analytics"
	"github.com/talyvor/track/internal/authz"
	"github.com/talyvor/track/internal/automation"
	"github.com/talyvor/track/internal/config"
	"github.com/talyvor/track/internal/customfield"
	"github.com/talyvor/track/internal/cycle"
	"github.com/talyvor/track/internal/db"
	"github.com/talyvor/track/internal/dbresil"
	"github.com/talyvor/track/internal/dependency"
	"github.com/talyvor/track/internal/featureboard"
	"github.com/talyvor/track/internal/gatewayauth"
	"github.com/talyvor/track/internal/guest"
	"github.com/talyvor/track/internal/health"
	"github.com/talyvor/track/internal/httpx"
	"github.com/talyvor/track/internal/importer"
	"github.com/talyvor/track/internal/integrations"
	"github.com/talyvor/track/internal/issue"
	"github.com/talyvor/track/internal/label"
	"github.com/talyvor/track/internal/lensintegration"
	"github.com/talyvor/track/internal/mcp"
	"github.com/talyvor/track/internal/metrics"
	"github.com/talyvor/track/internal/migrate"
	"github.com/talyvor/track/internal/milestone"
	"github.com/talyvor/track/internal/model"
	"github.com/talyvor/track/internal/notification"
	"github.com/talyvor/track/internal/project"
	"github.com/talyvor/track/internal/realtime"
	"github.com/talyvor/track/internal/scoring"
	"github.com/talyvor/track/internal/team"
	"github.com/talyvor/track/internal/template"
	"github.com/talyvor/track/internal/timetracking"
	"github.com/talyvor/track/internal/workflow"
	"github.com/talyvor/track/internal/workspace"
	"github.com/talyvor/track/migrations"
)

// runMigrate is the `track migrate <up|status>` subcommand: it applies or reports
// schema migrations against TRACK_DATABASE_URL and exits. It uses a SINGLE
// connection (not the pool) so the advisory lock that serializes concurrent
// runners lives on one session.
func runMigrate(args []string) {
	cmd := ""
	if len(args) > 0 {
		cmd = args[0]
	}
	dsn := os.Getenv("TRACK_DATABASE_URL")
	if dsn == "" {
		fmt.Fprintln(os.Stderr, "track migrate: TRACK_DATABASE_URL is required")
		os.Exit(2)
	}
	migs, err := migrate.Load(migrations.FS)
	if err != nil {
		fmt.Fprintln(os.Stderr, "track migrate:", err)
		os.Exit(1)
	}
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		fmt.Fprintln(os.Stderr, "track migrate: connect:", err)
		os.Exit(1)
	}
	defer conn.Close(ctx)

	switch cmd {
	case "up":
		applied, err := migrate.Up(ctx, conn, migs)
		if err != nil {
			fmt.Fprintln(os.Stderr, "track migrate up:", err)
			os.Exit(1)
		}
		if len(applied) == 0 {
			fmt.Println("migrate up: already up to date (0 applied)")
		} else {
			fmt.Printf("migrate up: applied %d migration(s): %s\n", len(applied), strings.Join(applied, ", "))
		}
	case "status":
		st, err := migrate.StatusOf(ctx, conn, migs)
		if err != nil {
			fmt.Fprintln(os.Stderr, "track migrate status:", err)
			os.Exit(1)
		}
		fmt.Printf("migrate status: %d applied, %d pending\n", len(st.Applied), len(st.Pending))
		for _, m := range st.Applied {
			fmt.Printf("  [x] %s\n", m.Name)
		}
		for _, m := range st.Pending {
			fmt.Printf("  [ ] %s\n", m.Name)
		}
	default:
		fmt.Fprintln(os.Stderr, "usage: track migrate <up|status>")
		os.Exit(2)
	}
}

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	slog.SetDefault(logger)

	// Subcommand: `track migrate up|status` runs the schema migrator and exits.
	// With no subcommand, track runs the API server (the default).
	if len(os.Args) > 1 && os.Args[1] == "migrate" {
		runMigrate(os.Args[2:])
		return
	}

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
	// T13 HA: opt-in Redis pub/sub so realtime events cross Track instances.
	// OFF by default — a single instance behaves exactly as before and never
	// touches Redis. When TRACK_HA_ENABLED is set, mirror events through
	// TRACK_REDIS_URL; a Redis blip degrades to local-only delivery, never a crash.
	if cfg.HAEnabled {
		opts, err := redis.ParseURL(cfg.RedisURL)
		if err != nil {
			slog.Error("redis url parse failed (TRACK_HA_ENABLED is set)", slog.String("err", err.Error()))
			os.Exit(1)
		}
		rdb := redis.NewClient(opts)
		defer func() { _ = rdb.Close() }()
		if err := rdb.Ping(ctx).Err(); err != nil {
			slog.Warn("redis ping failed; realtime HA will retry in the background", slog.String("err", err.Error()))
		}
		bridge := realtime.NewRedisBridge(rdb, hub, uuid.NewString(), true)
		hub.WithBridge(bridge)
		if err := bridge.Start(ctx); err != nil {
			slog.Warn("realtime: redis bridge subscribe failed; running local-only", slog.String("err", err.Error()))
		} else {
			slog.Info("realtime: redis HA fan-out enabled")
		}
		defer func() { _ = bridge.Close() }()
	}
	go hub.Run(ctx)
	notifier := realtime.NewNotifier(hub)

	workflowEngine := workflow.New(pool)
	templateStore := template.NewStore(pool)
	// Workspace creation auto-seeds the default templates so new
	// teams land into a workspace that already has Bug Report,
	// Feature Request, etc. ready to go.
	workspaceStore := workspace.NewStore(pool).WithTemplateSeeder(templateStore)
	customFieldStore := customfield.NewStore(pool)
	dependencyStore := dependency.NewStore(pool)
	timeStore := timetracking.NewStore(pool)
	scoringStore := scoring.NewStore(pool)
	// issueStore reads custom-field values, blocked indicator,
	// tracked time, and RICE/ICE scores when serving issues so REST
	// + MCP responses always include those fields without callers
	// having to stitch data from multiple stores.
	issueStore := issue.NewStore(pool).
		WithFieldFetcher(customFieldStore).
		WithBlockedChecker(dependencyStore).
		WithTimeTracker(timeStore).
		WithScorer(scoringStore)
	projectStore := project.NewStore(pool)
	cycleStore := cycle.NewStore(pool)
	notificationStore := notification.NewStore(pool)

	wsHandler := workspace.NewHandler(workspaceStore)
	teamHandler := team.NewHandler(team.NewStore(pool)).WithSeeder(workflowEngine)
	projectHandler := project.NewHandler(projectStore)
	issueHandler := issue.NewHandler(issueStore).
		WithNotifier(notifier).
		WithCustomFields(customFieldStore).
		WithTemplates(templateStore)
	customFieldHandler := customfield.NewHandler(customFieldStore)
	dependencyHandler := dependency.NewHandler(dependencyStore)
	timeHandler := timetracking.NewHandler(timeStore)
	templateHandler := template.NewHandler(templateStore)
	scoringHandler := scoring.NewHandler(scoringStore)
	// Public feature boards. issueStore is wired so the "Convert to
	// issue" admin action can spawn a Track issue from a post.
	featureBoardStore := featureboard.NewStore(pool)
	featureBoardHandler := featureboard.NewHandler(featureBoardStore, issueStore)
	// Guest store: invite + accept lives here; the access tokens are
	// stateless HMAC-signed. GUEST_SECRET seeds the HMAC key; empty
	// generates a per-process random key (fine for dev, never prod).
	guestStore := guest.NewStore(pool, os.Getenv("TRACK_GUEST_SECRET"))
	guestHandler := guest.NewHandler(guestStore, issueStore,
		os.Getenv("TRACK_INVITE_BASE_URL"))
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
	// T8 Build B — the async import-job spine. The runner drains pending jobs off-request (the sync
	// endpoints above stay as-is); mirrors the Start(ctx) idiom of the other background goroutines.
	importJobs := importer.NewJobStore(pool)
	importJobHandler := importer.NewJobHandler(importJobs)
	go importer.NewRunner(importJobs, importer.New(issueStore)).Start(ctx, 0)

	// T8 Build C.1 — per-workspace provider credential store (Linear/Jira API tokens, AES-256-GCM at rest).
	// Enabled ONLY when TRACK_INTEGRATION_ENCRYPTION_KEY is set (config already validated it decodes to 32
	// bytes at boot); absent ⇒ live API import is unavailable but Track runs normally.
	var integrationHandler *integrations.Handler
	if len(cfg.IntegrationEncryptionKey) > 0 {
		cipher, err := integrations.NewCipher(cfg.IntegrationEncryptionKey)
		if err != nil { // unreachable (config validated the length) — fail loud rather than run broken crypto
			slog.Error("integrations: cipher init failed", slog.String("err", err.Error()))
			os.Exit(1)
		}
		integrationHandler = integrations.NewHandler(integrations.NewStore(pool, cipher))
		slog.Info("integrations: provider credential store enabled")
	} else {
		slog.Info("integrations: TRACK_INTEGRATION_ENCRYPTION_KEY unset — live API import disabled")
	}

	// Preload rules for every workspace at startup so the first
	// matching event doesn't pay for an on-demand DB read.
	if ids, err := workspaceStore.ListIDs(ctx); err == nil {
		for _, ws := range ids {
			_ = automationEngine.LoadRules(ctx, ws)
		}
	}

	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.Recoverer)
	r.Use(middleware.Timeout(30 * time.Second))
	r.Use(metricsMiddleware)
	// Cap request bodies (oversize → 413) before any handler reads them — covers the
	// JSON API and the raw-body webhooks alike.
	r.Use(httpx.BodyLimit)

	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	})
	r.Handle("/metrics", metrics.Handler())

	// T14 liveness/readiness probes. Top-level and unauthenticated — like
	// /healthz and /metrics — so a load balancer can always reach them. The
	// static /healthz above is left untouched for backward-compat; these are
	// additive. /readyz pings Postgres, so during a DB outage it reports 503 and
	// the LB drains this instance instead of routing traffic to a broken one.
	// (When realtime HA / T13 is enabled, add a Redis dep here too.) drainer is
	// shared with the SIGTERM path below so /readyz flips to 503 on shutdown.
	drainer := &health.Drainer{}
	probes := health.New("0.1.0", drainer, health.PingDep("database", pool))
	r.Get("/livez", probes.Live)
	r.Get("/readyz", probes.Ready)

	// T15 DB-outage resilience: a circuit breaker fed by a background health
	// monitor. While Postgres is unreachable the breaker opens and the Guard
	// middleware on /v1 + /mcp fast-fails with 503 — a correct status instead of
	// the misleading 400 a raw connection error would otherwise produce, and with
	// no per-request hang against a dead pool. Complements /readyz (which drains
	// the instance from the load balancer) and the statement_timeout set in
	// db.New. Always on: when Postgres is healthy the breaker stays closed and the
	// Guard is a cheap atomic read.
	dbBreaker := dbresil.NewBreaker(dbresil.DefaultFailureThreshold).
		OnStateChange(func(open bool) {
			if open {
				slog.Error("db: circuit opened — Postgres unreachable; /v1 + /mcp now return 503")
			} else {
				slog.Info("db: circuit closed — Postgres reachable again")
			}
		})
	dbresil.NewMonitor(pool, dbBreaker, dbresil.DefaultProbeInterval, dbresil.DefaultPingTimeout).Start(ctx)

	// T9 + T10 auth chain, shared by the /v1 API and the MCP surface. T9: every request
	// must prove it transited the edge gateway (x-gateway-auth, constant-time-verified)
	// before any gateway-injected identity is trusted; exempt the own-auth paths that carry
	// no gateway identity (HMAC webhooks, the anonymous public board, guest-token + invite
	// routes, the websocket). T10: resolve the verified email -> memberships and, for a
	// {wsID} route, require membership (else 403), putting the authorized workspace + member
	// in context. Hoisted so both /v1 and /mcp use the same instances.
	gwExempt := func(p string) bool {
		return strings.HasPrefix(p, "/v1/lens/webhook") ||
			strings.HasPrefix(p, "/v1/webhooks/") ||
			strings.HasPrefix(p, "/v1/public/") ||
			strings.HasPrefix(p, "/v1/invite/") ||
			strings.HasPrefix(p, "/v1/guest/") ||
			p == "/v1/ws"
	}
	gwAuth := gatewayauth.Middleware(cfg.GatewayAuthSecret, gwExempt)
	wsAuthz := authz.Middleware(authz.NewPGResolver(pool), gwExempt)

	// MCP (T11b): behind the SAME chain as /v1 — a request reaches a tool only with a valid
	// transit proof + verified identity (no /mcp gets x-gateway-auth + x-user-email like
	// /v1). /mcp/sse (transport: endpoint event + pings) gets the proof too. Per-tool
	// workspace authorization is enforced inside HandleRPC's chokepoint (handleToolsCall),
	// since each tool's workspace_id is a JSON-RPC argument, not a path param.
	r.Group(func(r chi.Router) {
		r.Use(dbresil.Guard(dbBreaker))
		r.Use(gwAuth)
		r.Use(wsAuthz)
		r.Post("/mcp", mcpServer.HandleRPC)
		r.Get("/mcp/sse", mcpServer.HandleSSE)
	})

	r.Route("/v1", func(r chi.Router) {
		// DB-outage guard runs first: if Postgres is down, fast-fail 503 before
		// auth (which itself reads the DB for membership resolution).
		r.Use(dbresil.Guard(dbBreaker))
		r.Use(gwAuth)
		r.Use(wsAuthz)
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
		importJobHandler.Mount(r) // T8 Build B: async POST /import/jobs + GET /import/jobs/{id}
		if integrationHandler != nil {
			integrationHandler.Mount(r) // T8 Build C.1: POST /integrations + GET /integrations/{provider}
		}
		customFieldHandler.Mount(r)
		dependencyHandler.Mount(r)
		timeHandler.Mount(r)
		templateHandler.Mount(r)
		scoringHandler.Mount(r)
		guestHandler.Mount(r)
		featureBoardHandler.Mount(r)

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
	// Graceful drain: flip /readyz to 503 so the load balancer stops sending new
	// requests and pulls this instance from rotation, pause briefly so it
	// observes the change, then stop accepting and let in-flight requests finish
	// before the pool closes (deferred above).
	if err := drainer.Drain(shutdownCtx, 2*time.Second, srv.Shutdown); err != nil {
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
