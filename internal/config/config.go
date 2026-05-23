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
}

func Load() (*Config, error) {
	c := &Config{
		ListenAddr:  getEnv("TRACK_LISTEN_ADDR", "0.0.0.0:3000"),
		DatabaseURL: os.Getenv("TRACK_DATABASE_URL"),
		LogLevel:    getEnv("TRACK_LOG_LEVEL", "info"),
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
