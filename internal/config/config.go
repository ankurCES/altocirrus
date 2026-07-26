package config

import (
	"os"
	"strconv"
)

// Config holds all configuration for the AltoCirrus emulator.
type Config struct {
	Port  int
	Azure AzureConfig
	GCP   GCPConfig
}

// AzureConfig holds Azure-specific emulator configuration.
type AzureConfig struct {
	SubscriptionID string
	TenantID       string
	Region         string
}

// GCPConfig holds GCP-specific emulator configuration.
type GCPConfig struct {
	ProjectID     string
	ProjectNumber string
	Region        string
}

// Load reads configuration from environment variables, applying defaults
// for any values not set.
func Load() *Config {
	return &Config{
		Port: envInt("ALTOCIRRUS_PORT", 4567),
		Azure: AzureConfig{
			SubscriptionID: envStr("ALTOCIRRUS_AZURE_SUBSCRIPTION_ID", "00000000-0000-0000-0000-000000000000"),
			TenantID:       envStr("ALTOCIRRUS_AZURE_TENANT_ID", "00000000-0000-0000-0000-000000000001"),
			Region:         envStr("ALTOCIRRUS_AZURE_REGION", "eastus"),
		},
		GCP: GCPConfig{
			ProjectID:     envStr("ALTOCIRRUS_GCP_PROJECT_ID", "local-project"),
			ProjectNumber: envStr("ALTOCIRRUS_GCP_PROJECT_NUMBER", "123456789"),
			Region:        envStr("ALTOCIRRUS_GCP_REGION", "us-central1"),
		},
	}
}

func envStr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func envInt(key string, fallback int) int {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return fallback
	}
	return n
}
