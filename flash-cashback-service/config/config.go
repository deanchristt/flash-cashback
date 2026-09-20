// Package config loads runtime configuration from the environment. Twelve-factor
// style: no config files baked into the image, everything injected at deploy.
package config

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

type Config struct {
	Env  string // "dev" | "prod"; affects log format
	Port string

	DatabaseURL string
	RedisAddr     string
	RedisPassword string
	RedisDB       int

	// CampaignTotalBudget seeds the campaign_budget row the first time the
	// service starts against a fresh database. Once seeded, the database row is
	// the source of truth and this value is ignored.
	CampaignTotalBudget int64

	// RateLimitPerMinute caps write requests per user per minute (0 disables).
	RateLimitPerMinute int

	ReadTimeout     time.Duration
	WriteTimeout    time.Duration
	ShutdownTimeout time.Duration
}

func Load() (Config, error) {
	c := Config{
		Env:                 getenv("APP_ENV", "dev"),
		Port:                getenv("PORT", "8080"),
		DatabaseURL:         getenv("DATABASE_URL", "postgres://cashback:cashback@localhost:5432/cashback?sslmode=disable"),
		RedisAddr:           getenv("REDIS_ADDR", "localhost:6379"),
		RedisPassword:       getenv("REDIS_PASSWORD", ""),
		RedisDB:             getenvInt("REDIS_DB", 0),
		CampaignTotalBudget: getenvInt64("CAMPAIGN_TOTAL_BUDGET", 10_000_000),
		RateLimitPerMinute:  getenvInt("RATE_LIMIT_PER_MINUTE", 60),
		ReadTimeout:         getenvDuration("READ_TIMEOUT", 10*time.Second),
		WriteTimeout:        getenvDuration("WRITE_TIMEOUT", 10*time.Second),
		ShutdownTimeout:     getenvDuration("SHUTDOWN_TIMEOUT", 15*time.Second),
	}
	if c.CampaignTotalBudget <= 0 {
		return c, fmt.Errorf("CAMPAIGN_TOTAL_BUDGET must be positive, got %d", c.CampaignTotalBudget)
	}
	return c, nil
}

func getenv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func getenvInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func getenvInt64(key string, def int64) int64 {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			return n
		}
	}
	return def
}

func getenvDuration(key string, def time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return def
}
