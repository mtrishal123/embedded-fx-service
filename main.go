// main.go — the entry point for the fx-settlement service.
//
// This is where everything gets wired together:
//   - Config from environment variables (12-factor app style)
//   - Repository (postgres in prod, memory for local dev without DB)
//   - FX converter
//   - Settlement service
//   - HTTP server with graceful shutdown
//
// Run locally (no DB):  go run ./cmd/server
// Run with Postgres:    DB_DSN="host=localhost..." go run ./cmd/server
package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gorilla/mux"

	"github.com/trishal/fx-settlement/internal/api"
	"github.com/trishal/fx-settlement/internal/fx"
	"github.com/trishal/fx-settlement/internal/settlement"
)

func main() {
	port := getEnv("PORT", "8080")
	dbDSN := getEnv("DB_DSN", "") // empty = use in-memory repo

	// -------------------------------------------------------------------
	// Wire up dependencies
	// -------------------------------------------------------------------

	// FX rate provider (static for now; swap for ECBRateProvider in prod)
	rateProvider := fx.NewStaticRateProvider()
	converter := fx.NewConverter(rateProvider)

	// Repository — postgres if DSN provided, otherwise in-memory
	var repo settlement.Repository
	if dbDSN != "" {
		pgRepo, err := settlement.NewPostgresRepository(dbDSN)
		if err != nil {
			log.Fatalf("connect to postgres: %v", err)
		}
		repo = pgRepo
		log.Println("using PostgreSQL repository")
	} else {
		repo = settlement.NewMemoryRepository()
		log.Println("using in-memory repository (data will not persist)")
	}

	svc := settlement.NewService(converter, repo)
	handler := api.NewHandler(svc, rateProvider)

	// -------------------------------------------------------------------
	// HTTP server
	// -------------------------------------------------------------------

	r := mux.NewRouter()
	handler.RegisterRoutes(r)

	// Middleware: log every request
	r.Use(loggingMiddleware)

	srv := &http.Server{
		Addr:         fmt.Sprintf(":%s", port),
		Handler:      r,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	// Start server in a goroutine so we can listen for shutdown signals
	go func() {
		log.Printf("fx-settlement service listening on :%s", port)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("server error: %v", err)
		}
	}()

	// -------------------------------------------------------------------
	// Graceful shutdown — important for Kubernetes rolling deployments
	// -------------------------------------------------------------------

	// Wait for SIGINT (Ctrl+C) or SIGTERM (k8s pod termination)
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	log.Println("shutting down gracefully...")

	// Give in-flight requests 30 seconds to complete
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := srv.Shutdown(ctx); err != nil {
		log.Fatalf("forced shutdown: %v", err)
	}

	log.Println("server stopped")
}

func loggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		log.Printf("%s %s — %s", r.Method, r.URL.Path, time.Since(start))
	})
}

func getEnv(key, fallback string) string {
	if val := os.Getenv(key); val != "" {
		return val
	}
	return fallback
}
