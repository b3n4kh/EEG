package config

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"os"
)

type Config struct {
	Addr          string
	DatabasePath  string
	SessionSecret string
	AdminUsername string
	AdminPassword string
	AdminAPIToken string
	DevMode       bool
}

func Load() (Config, error) {
	cfg := Config{
		Addr:          env("ADDR", ":8080"),
		DatabasePath:  env("DATABASE_PATH", "./data/eeg.db"),
		SessionSecret: os.Getenv("SESSION_SECRET"),
		AdminUsername: os.Getenv("ADMIN_USERNAME"),
		AdminPassword: os.Getenv("ADMIN_PASSWORD"),
		AdminAPIToken: os.Getenv("ADMIN_API_TOKEN"),
		DevMode:       env("APP_ENV", "dev") == "dev",
	}
	if cfg.SessionSecret == "" {
		if !cfg.DevMode {
			return Config{}, fmt.Errorf("SESSION_SECRET is required outside dev")
		}
		secret := make([]byte, 32)
		if _, err := rand.Read(secret); err != nil {
			return Config{}, fmt.Errorf("generate development session secret: %w", err)
		}
		cfg.SessionSecret = base64.StdEncoding.EncodeToString(secret)
	}
	return cfg, nil
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
