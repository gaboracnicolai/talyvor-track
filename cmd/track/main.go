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

	"github.com/talyvor/track/internal/config"
	"github.com/talyvor/track/internal/db"
	"github.com/talyvor/track/internal/issue"
	"github.com/talyvor/track/internal/metrics"
	"github.com/talyvor/track/internal/project"
	"github.com/talyvor/track/internal/team"
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
	wsHandler := workspace.NewHandler(workspace.NewStore(pool))
	teamHandler := team.NewHandler(team.NewStore(pool))
	projectHandler := project.NewHandler(project.NewStore(pool))
	issueHandler := issue.NewHandler(issue.NewStore(pool))

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

	r.Route("/v1", func(r chi.Router) {
		wsHandler.Mount(r)
		teamHandler.Mount(r)
		projectHandler.Mount(r)
		issueHandler.Mount(r)
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
