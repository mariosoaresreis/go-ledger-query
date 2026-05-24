// Package main is the query-service composition root.
// It wires every layer together and starts two concurrent loops:
//  1. Kafka consumer → projection updates (writes to read DB)
//  2. HTTP server    → query responses  (reads from read DB)
package main

import (
	"context"
	"errors"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/zap"

	kafkaadapter "github.com/mariosoaresreis/go-ledger-query/internal/adapters/kafka"
	pgadapter "github.com/mariosoaresreis/go-ledger-query/internal/adapters/postgres"
	"github.com/mariosoaresreis/go-ledger-query/internal/application"
	"github.com/mariosoaresreis/go-ledger-query/internal/docs"
	"github.com/mariosoaresreis/go-ledger-query/internal/handler"
	"github.com/mariosoaresreis/go-ledger-query/internal/observability"
)

func main() {
	log := mustLogger()
	defer log.Sync() //nolint:errcheck

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// ── Observability ─────────────────────────────────────────────────────────
	shutdownTracing, err := observability.SetupTracing(ctx, "ledger-query")
	if err != nil {
		log.Fatal("tracing setup failed", zap.Error(err))
	}
	defer shutdownTracing()

	// ── Infrastructure ────────────────────────────────────────────────────────
	pool := mustPool(ctx, mustEnv("DATABASE_URL"), log)
	defer pool.Close()

	brokers := []string{mustEnv("KAFKA_BROKER")}

	// ── Wire layers ───────────────────────────────────────────────────────────
	//
	//  Kafka msg → Consumer → Dispatcher → ReadRepository (postgres upsert)
	//  HTTP GET  → Handler  → QueryService → ReadRepository (postgres select)
	//
	repo := pgadapter.New(pool)
	dispatcher := kafkaadapter.NewDispatcher(repo, log)
	consumer := kafkaadapter.NewConsumer(brokers, dispatcher, log)
	svc := application.New(repo, log)
	h := handler.New(svc, log)

	// ── Kafka consumer (runs in background goroutine) ─────────────────────────
	consumerDone := make(chan error, 1)
	go func() {
		consumerDone <- consumer.Start(ctx)
	}()
	defer func() {
		if err := consumer.Close(); err != nil {
			log.Error("consumer close error", zap.Error(err))
		}
	}()

	// ── HTTP server ───────────────────────────────────────────────────────────
	gin.SetMode(envOr("GIN_MODE", "release"))
	r := gin.New()
	r.Use(gin.Recovery(), requestLogger(log), observability.GinMetricsMiddleware())

	r.GET("/health", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok", "service": "ledger-query"})
	})
	r.GET("/ready", func(c *gin.Context) {
		if err := pool.Ping(c.Request.Context()); err != nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"status": "ready"})
	})
	// Prometheus metrics endpoint — scraped by prometheus or any OTEL collector.
	r.GET("/metrics", gin.WrapH(observability.MetricsHandler()))
	r.GET("/swagger-doc.json", func(c *gin.Context) {
		c.Data(http.StatusOK, "application/json; charset=utf-8", docs.QueryOpenAPI())
	})
	r.GET("/swagger", func(c *gin.Context) {
		c.Data(http.StatusOK, "text/html; charset=utf-8", []byte(swaggerUIHTML("/swagger-doc.json")))
	})
	r.GET("/swagger/index.html", func(c *gin.Context) {
		c.Data(http.StatusOK, "text/html; charset=utf-8", []byte(swaggerUIHTML("/swagger-doc.json")))
	})

	h.Register(r.Group("/v1"))

	srv := &http.Server{
		Addr:         ":" + envOr("PORT", "8081"),
		Handler:      r,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	go func() {
		log.Info("ledger-query starting", zap.String("addr", srv.Addr))
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatal("server error", zap.Error(err))
		}
	}()

	// ── Wait for shutdown signal or consumer crash ─────────────────────────────
	select {
	case <-ctx.Done():
		log.Info("shutdown signal received")
	case err := <-consumerDone:
		if err != nil {
			log.Error("consumer exited unexpectedly", zap.Error(err))
		}
	}

	shutCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutCtx); err != nil {
		log.Error("graceful HTTP shutdown failed", zap.Error(err))
	}
	log.Info("server stopped")
}

// ── helpers ───────────────────────────────────────────────────────────────────

func mustPool(ctx context.Context, dsn string, log *zap.Logger) *pgxpool.Pool {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		log.Fatal("pg config", zap.Error(err))
	}
	cfg.MaxConns = 20
	cfg.MinConns = 3
	cfg.MaxConnLifetime = 30 * time.Minute
	cfg.MaxConnIdleTime = 5 * time.Minute

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		log.Fatal("pg connect", zap.Error(err))
	}
	if err := pool.Ping(ctx); err != nil {
		log.Fatal("pg ping", zap.Error(err))
	}
	log.Info("postgres connected")
	return pool
}

func mustLogger() *zap.Logger {
	l, err := zap.NewProduction()
	if err != nil {
		panic(err)
	}
	return l
}

func mustEnv(key string) string {
	v := os.Getenv(key)
	if v == "" {
		panic("required env var not set: " + key)
	}
	return v
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func requestLogger(log *zap.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		c.Next()
		log.Info("http",
			zap.String("method", c.Request.Method),
			zap.String("path", c.FullPath()),
			zap.Int("status", c.Writer.Status()),
			zap.Duration("latency", time.Since(start)),
			zap.String("ip", c.ClientIP()),
		)
	}
}

func swaggerUIHTML(specPath string) string {
	return strings.ReplaceAll(`<!doctype html>
<html>
<head>
  <meta charset="utf-8" />
  <title>Swagger UI</title>
  <link rel="stylesheet" href="https://unpkg.com/swagger-ui-dist@5/swagger-ui.css" />
</head>
<body>
  <div id="swagger-ui"></div>
  <script src="https://unpkg.com/swagger-ui-dist@5/swagger-ui-bundle.js"></script>
  <script>
    window.ui = SwaggerUIBundle({
      url: "__SPEC_PATH__",
      dom_id: "#swagger-ui"
    });
  </script>
</body>
</html>
`, "__SPEC_PATH__", specPath)
}
