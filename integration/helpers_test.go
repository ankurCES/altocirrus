//go:build integration

package integration_test

import (
	"net/http/httptest"
	"os"
	"testing"

	"github.com/altocirrus/altocirrus/internal/azure/arm"
	azureauth "github.com/altocirrus/altocirrus/internal/azure/auth"
	"github.com/altocirrus/altocirrus/internal/azure/blobstorage"
	"github.com/altocirrus/altocirrus/internal/azure/cosmosdb"
	"github.com/altocirrus/altocirrus/internal/azure/keyvault"
	"github.com/altocirrus/altocirrus/internal/azure/queuestorage"
	"github.com/altocirrus/altocirrus/internal/config"
	gcpauth "github.com/altocirrus/altocirrus/internal/gcp/auth"
	"github.com/altocirrus/altocirrus/internal/gcp/firestore"
	"github.com/altocirrus/altocirrus/internal/gcp/pubsub"
	"github.com/altocirrus/altocirrus/internal/gcp/secretmanager"
	gcpstorage "github.com/altocirrus/altocirrus/internal/gcp/storage"
	"github.com/altocirrus/altocirrus/internal/server"
	"github.com/altocirrus/altocirrus/internal/storage"
)

const gcpProject = "local-project"

var (
	testServer    *httptest.Server
	tlsTestServer *httptest.Server // HTTPS variant — required by azidentity (enforces HTTPS authority)
	testCfg       *config.Config
)

func TestMain(m *testing.M) {
	testCfg = &config.Config{
		Azure: config.AzureConfig{
			SubscriptionID: "00000000-0000-0000-0000-000000000000",
			TenantID:       "00000000-0000-0000-0000-000000000001",
			Region:         "eastus",
		},
		GCP: config.GCPConfig{
			ProjectID:     gcpProject,
			ProjectNumber: "123456789",
			Region:        "us-central1",
		},
	}

	store := storage.NewMemoryStore()
	mux := server.New(testCfg, store)

	azureauth.RegisterRoutes(mux, testCfg)
	keyvault.RegisterRoutes(mux, store, testCfg)
	arm.RegisterRoutes(mux, store, testCfg)
	blobstorage.RegisterRoutes(mux, store, testCfg)
	queuestorage.RegisterRoutes(mux, store, testCfg)
	gcpauth.RegisterRoutes(mux, testCfg)
	secretmanager.RegisterRoutes(mux, store, testCfg)
	gcpstorage.RegisterRoutes(mux, store, testCfg)
	pubsub.RegisterRoutes(mux, store, testCfg)
	cosmosdb.RegisterRoutes(mux, store, testCfg)
	firestore.RegisterRoutes(mux, store, testCfg)
	server.RegisterAdminRoutes(mux, store)

	handler := server.LoggingMiddleware(server.CORSMiddleware(mux))
	testServer = httptest.NewServer(handler)
	defer testServer.Close()
	tlsTestServer = httptest.NewTLSServer(handler)
	defer tlsTestServer.Close()

	os.Exit(m.Run())
}
