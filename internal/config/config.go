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
}

func Load() (*Config, error) {
	c := &Config{
		ListenAddr:        getEnv("TRACK_LISTEN_ADDR", "0.0.0.0:3000"),
		DatabaseURL:       os.Getenv("TRACK_DATABASE_URL"),
		LogLevel:          getEnv("TRACK_LOG_LEVEL", "info"),
		LensURL:           os.Getenv("TRACK_LENS_URL"),
		LensAPIKey:        os.Getenv("TRACK_LENS_API_KEY"),
		LensWebhookSecret: os.Getenv("TRACK_LENS_WEBHOOK_SECRET"),
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
