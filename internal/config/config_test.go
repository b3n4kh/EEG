package config

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ben/eeg-sumsum/internal/eda"
)

func TestLoadUsesDevelopmentDefaultsAndGeneratesSessionSecret(t *testing.T) {
	clearConfigEnv(t)
	cfg, err := Load()
	require.NoError(t, err)
	require.Equal(t, ":8080", cfg.Addr)
	require.Equal(t, "./data/eeg.db", cfg.DatabasePath)
	require.True(t, cfg.DevMode)
	require.NotEmpty(t, cfg.SessionSecret)
	require.Equal(t, eda.DefaultBaseURL, cfg.EDA.BaseURL)
	require.True(t, cfg.EDAAutoImport.Enabled)
	require.Equal(t, "15 3 * * *", cfg.EDAAutoImport.Schedule)
}

func TestLoadRequiresSessionSecretOutsideDev(t *testing.T) {
	clearConfigEnv(t)
	t.Setenv("APP_ENV", "production")
	_, err := Load()
	require.EqualError(t, err, "SESSION_SECRET is required outside dev")
}

func TestLoadReadsOverrides(t *testing.T) {
	clearConfigEnv(t)
	t.Setenv("APP_ENV", "production")
	t.Setenv("ADDR", ":9090")
	t.Setenv("DATABASE_PATH", "/tmp/eeg.db")
	t.Setenv("SESSION_SECRET", "test-secret")
	t.Setenv("ADMIN_USERNAME", "admin")
	t.Setenv("ADMIN_PASSWORD", "admin-password")
	t.Setenv("ADMIN_API_TOKEN", "api-token")
	t.Setenv("EDA_BASE_URL", "https://eda.example.test/api")
	t.Setenv("EDA_USERNAME", "eda-user")
	t.Setenv("EDA_PASSWORD", "eda-pass")
	t.Setenv("EDA_COMMUNITY_ID", "community")
	t.Setenv("EDA_METERING_POINTS", "AT001:CONSUMPTION")
	t.Setenv("EDA_AUTO_IMPORT_ENABLED", "false")
	t.Setenv("EDA_AUTO_IMPORT_CRON", "45 2 * * *")

	cfg, err := Load()
	require.NoError(t, err)
	require.False(t, cfg.DevMode)
	require.Equal(t, ":9090", cfg.Addr)
	require.Equal(t, "/tmp/eeg.db", cfg.DatabasePath)
	require.Equal(t, "test-secret", cfg.SessionSecret)
	require.Equal(t, "admin", cfg.AdminUsername)
	require.Equal(t, "admin-password", cfg.AdminPassword)
	require.Equal(t, "api-token", cfg.AdminAPIToken)
	require.Equal(t, "https://eda.example.test/api", cfg.EDA.BaseURL)
	require.Equal(t, "eda-user", cfg.EDA.Username)
	require.Equal(t, "eda-pass", cfg.EDA.Password)
	require.Equal(t, "community", cfg.EDA.CommunityID)
	require.Equal(t, "AT001:CONSUMPTION", cfg.EDA.MeteringPoints)
	require.False(t, cfg.EDAAutoImport.Enabled)
	require.Equal(t, "45 2 * * *", cfg.EDAAutoImport.Schedule)
}

func clearConfigEnv(t *testing.T) {
	t.Helper()
	for _, key := range []string{
		"ADDR",
		"DATABASE_PATH",
		"SESSION_SECRET",
		"ADMIN_USERNAME",
		"ADMIN_PASSWORD",
		"ADMIN_API_TOKEN",
		"APP_ENV",
		"EDA_BASE_URL",
		"EDA_USERNAME",
		"EDA_PASSWORD",
		"EDA_COMMUNITY_ID",
		"EDA_METERING_POINTS",
		"EDA_AUTO_IMPORT_ENABLED",
		"EDA_AUTO_IMPORT_CRON",
	} {
		t.Setenv(key, "")
	}
}
