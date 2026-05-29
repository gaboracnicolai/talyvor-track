// Package config loads runtime configuration from environment vars.
// The Track server is intentionally minimal in this phase — Postgres
// URL is mandatory, everything else has a sensible default.
package config

import (
	"errors"
	"fmt"
	"os"
)

type Config struct {
	ListenAddr  string
	DatabaseURL string
	LogLevel    string

	// Talyvor Lens integration. All three are optional — an empty
	// LensURL keeps Track running in standalone mode (no AI cost
	// data, but every other endpoint works).
	LensURL           string
	LensAPIKey        string
	LensWebhookSecret string

	// AppBaseURL is the public base URL used to build deep links in
	// notification emails (e.g. https://track.acme.com/issues/ENG-42).
	// Email itself is configured via the product-neutral EMAIL_* vars
	// read by internal/email.
	AppBaseURL string
}

func Load() (*Config, error) {
	c := &Config{
		ListenAddr:        getEnv("TRACK_LISTEN_ADDR", "0.0.0.0:3000"),
		DatabaseURL:       os.Getenv("TRACK_DATABASE_URL"),
		LogLevel:          getEnv("TRACK_LOG_LEVEL", "info"),
		LensURL:           os.Getenv("TRACK_LENS_URL"),
		LensAPIKey:        os.Getenv("TRACK_LENS_API_KEY"),
		LensWebhookSecret: os.Getenv("TRACK_LENS_WEBHOOK_SECRET"),
		AppBaseURL:        getEnv("TRACK_APP_BASE_URL", "http://localhost:3000"),
	}
	if c.DatabaseURL == "" {
		return nil, fmt.Errorf("%w: TRACK_DATABASE_URL", ErrMissingEnv)
	}
	return c, nil
}

var ErrMissingEnv = errors.New("missing required environment variable")

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
