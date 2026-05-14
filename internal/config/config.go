package config

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"os"

	"github.com/ben/eeg-sumsum/internal/eda"
)

type Config struct {
	Addr          string
	DatabasePath  string
	SessionSecret string
	AdminUsername string
	AdminPassword string
	AdminAPIToken string
	EDA           eda.Config
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
		EDA: eda.Config{
			BaseURL:           env("EDA_BASE_URL", eda.DefaultBaseURL),
			Username:          os.Getenv("EDA_USERNAME"),
			Password:          os.Getenv("EDA_PASSWORD"),
			CommunityID:       os.Getenv("EDA_COMMUNITY_ID"),
			MeteringPointID:   os.Getenv("EDA_METERING_POINT_ID"),
			MeteringPointName: os.Getenv("EDA_METERING_POINT_NAME"),
			GroupBy:           env("EDA_GROUP_BY", "day"),
		},
		DevMode: env("APP_ENV", "dev") == "dev",
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
