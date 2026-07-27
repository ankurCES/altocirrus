package config

import "testing"

func TestDefaults(t *testing.T) {
	for _, k := range []string{
		"ALTOCIRRUS_PORT", "ALTOCIRRUS_STORAGE", "ALTOCIRRUS_DB_PATH",
		"ALTOCIRRUS_AZURE_SUBSCRIPTION_ID", "ALTOCIRRUS_AZURE_TENANT_ID", "ALTOCIRRUS_AZURE_REGION",
		"ALTOCIRRUS_GCP_PROJECT_ID", "ALTOCIRRUS_GCP_PROJECT_NUMBER", "ALTOCIRRUS_GCP_REGION",
	} {
		t.Setenv(k, "")
	}

	cfg := Load()

	if cfg.Port != 4567 {
		t.Errorf("Port = %d, want 4567", cfg.Port)
	}
	if cfg.Storage != "memory" {
		t.Errorf("Storage = %q, want memory", cfg.Storage)
	}
	if cfg.DBPath != "./altocirrus.db" {
		t.Errorf("DBPath = %q, want ./altocirrus.db", cfg.DBPath)
	}
	if cfg.Azure.SubscriptionID != "00000000-0000-0000-0000-000000000000" {
		t.Errorf("Azure.SubscriptionID = %q", cfg.Azure.SubscriptionID)
	}
	if cfg.Azure.TenantID != "00000000-0000-0000-0000-000000000001" {
		t.Errorf("Azure.TenantID = %q", cfg.Azure.TenantID)
	}
	if cfg.Azure.Region != "eastus" {
		t.Errorf("Azure.Region = %q, want eastus", cfg.Azure.Region)
	}
	if cfg.GCP.ProjectID != "local-project" {
		t.Errorf("GCP.ProjectID = %q, want local-project", cfg.GCP.ProjectID)
	}
	if cfg.GCP.ProjectNumber != "123456789" {
		t.Errorf("GCP.ProjectNumber = %q", cfg.GCP.ProjectNumber)
	}
	if cfg.GCP.Region != "us-central1" {
		t.Errorf("GCP.Region = %q, want us-central1", cfg.GCP.Region)
	}
}

func TestEnvOverrides(t *testing.T) {
	t.Setenv("ALTOCIRRUS_PORT", "8080")
	t.Setenv("ALTOCIRRUS_STORAGE", "sqlite")
	t.Setenv("ALTOCIRRUS_DB_PATH", "/tmp/test.db")
	t.Setenv("ALTOCIRRUS_AZURE_SUBSCRIPTION_ID", "sub-123")
	t.Setenv("ALTOCIRRUS_AZURE_TENANT_ID", "tenant-456")
	t.Setenv("ALTOCIRRUS_AZURE_REGION", "westus")
	t.Setenv("ALTOCIRRUS_GCP_PROJECT_ID", "my-proj")
	t.Setenv("ALTOCIRRUS_GCP_PROJECT_NUMBER", "999")
	t.Setenv("ALTOCIRRUS_GCP_REGION", "us-east1")

	cfg := Load()

	if cfg.Port != 8080 {
		t.Errorf("Port = %d, want 8080", cfg.Port)
	}
	if cfg.Storage != "sqlite" {
		t.Errorf("Storage = %q, want sqlite", cfg.Storage)
	}
	if cfg.DBPath != "/tmp/test.db" {
		t.Errorf("DBPath = %q, want /tmp/test.db", cfg.DBPath)
	}
	if cfg.Azure.SubscriptionID != "sub-123" {
		t.Errorf("Azure.SubscriptionID = %q", cfg.Azure.SubscriptionID)
	}
	if cfg.Azure.TenantID != "tenant-456" {
		t.Errorf("Azure.TenantID = %q", cfg.Azure.TenantID)
	}
	if cfg.Azure.Region != "westus" {
		t.Errorf("Azure.Region = %q", cfg.Azure.Region)
	}
	if cfg.GCP.ProjectID != "my-proj" {
		t.Errorf("GCP.ProjectID = %q", cfg.GCP.ProjectID)
	}
	if cfg.GCP.ProjectNumber != "999" {
		t.Errorf("GCP.ProjectNumber = %q", cfg.GCP.ProjectNumber)
	}
	if cfg.GCP.Region != "us-east1" {
		t.Errorf("GCP.Region = %q", cfg.GCP.Region)
	}
}

func TestInvalidPort(t *testing.T) {
	t.Setenv("ALTOCIRRUS_PORT", "not-a-number")
	cfg := Load()
	if cfg.Port != 4567 {
		t.Errorf("invalid port: got %d, want default 4567", cfg.Port)
	}
}
