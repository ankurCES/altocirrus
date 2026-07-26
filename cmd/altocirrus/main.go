package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/altocirrus/altocirrus/internal/azure/arm"
	azureauth "github.com/altocirrus/altocirrus/internal/azure/auth"
	"github.com/altocirrus/altocirrus/internal/azure/blobstorage"
	"github.com/altocirrus/altocirrus/internal/azure/cosmosdb"
	"github.com/altocirrus/altocirrus/internal/azure/keyvault"
	"github.com/altocirrus/altocirrus/internal/config"
	gcpauth "github.com/altocirrus/altocirrus/internal/gcp/auth"
	"github.com/altocirrus/altocirrus/internal/gcp/firestore"
	"github.com/altocirrus/altocirrus/internal/gcp/pubsub"
	"github.com/altocirrus/altocirrus/internal/gcp/secretmanager"
	gcpstorage "github.com/altocirrus/altocirrus/internal/gcp/storage"
	"github.com/altocirrus/altocirrus/internal/server"
	"github.com/altocirrus/altocirrus/internal/storage"
)

func main() {
	cfg := config.Load()

	var store storage.Store
	if cfg.Storage == "sqlite" {
		sqlStore, err := storage.NewSQLiteStore(cfg.DBPath)
		if err != nil {
			slog.Error("failed to open sqlite store", "error", err, "path", cfg.DBPath)
			os.Exit(1)
		}
		defer sqlStore.Close()
		store = sqlStore
		slog.Info("using sqlite storage", "path", cfg.DBPath)
	} else {
		store = storage.NewMemoryStore()
		slog.Info("using in-memory storage")
	}

	mux := server.New(cfg, store)

	// Register all service routes.
	azureauth.RegisterRoutes(mux, cfg)
	keyvault.RegisterRoutes(mux, store, cfg)
	arm.RegisterRoutes(mux, store, cfg)
	blobstorage.RegisterRoutes(mux, store, cfg)
	gcpauth.RegisterRoutes(mux, cfg)
	secretmanager.RegisterRoutes(mux, store, cfg)
	gcpstorage.RegisterRoutes(mux, store, cfg)
	pubsub.RegisterRoutes(mux, store, cfg)
	cosmosdb.RegisterRoutes(mux, store, cfg)
	firestore.RegisterRoutes(mux, store, cfg)
	server.RegisterAdminRoutes(mux, store)

	handler := server.LoggingMiddleware(server.CORSMiddleware(mux))

	addr := fmt.Sprintf(":%d", cfg.Port)
	srv := &http.Server{
		Addr:         addr,
		Handler:      handler,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	go func() {
		slog.Info("altocirrus starting",
			"port", cfg.Port,
			"azure_region", cfg.Azure.Region,
			"gcp_region", cfg.GCP.Region,
		)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("server failed", "error", err)
			os.Exit(1)
		}
	}()

	<-ctx.Done()
	slog.Info("shutting down gracefully")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		slog.Error("shutdown error", "error", err)
		os.Exit(1)
	}

	slog.Info("altocirrus stopped")
}
