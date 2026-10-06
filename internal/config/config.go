// Package config loads runtime settings from environment variables.
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

type Config struct {
	Addr           string
	DatabaseURL    string
	AdminToken     string
	AllowedOrigins []string
	MaxEPUBBytes   int64
	MaxImageBytes  int64
}

func Load() (Config, error) {
	cfg := Config{
		Addr:           env("ADDR", ":8080"),
		DatabaseURL:    env("DATABASE_URL", "postgres://postgres:postgres@localhost:5432/readly?sslmode=disable"),
		AdminToken:     os.Getenv("ADMIN_TOKEN"),
		AllowedOrigins: splitList(env("ALLOWED_ORIGINS", "http://localhost:3000,http://localhost:3001")),
		MaxEPUBBytes:   envInt("MAX_EPUB_MB", 100) << 20,
		MaxImageBytes:  envInt("MAX_IMAGE_MB", 10) << 20,
	}
	if cfg.AdminToken == "" {
		return cfg, fmt.Errorf("ADMIN_TOKEN must be set")
	}
	return cfg, nil
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envInt(key string, def int64) int64 {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
			return n
		}
	}
	return def
}

func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
