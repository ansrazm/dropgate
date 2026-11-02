package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ansrazm/dropgate/internal/api"
	"github.com/ansrazm/dropgate/internal/db"
	"github.com/ansrazm/dropgate/internal/events"
	"github.com/ansrazm/dropgate/internal/store"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(log); err != nil {
		log.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	dsn := env("DATABASE_URL", "postgres://dropgate:dropgate@localhost:5434/dropgate?sslmode=disable")
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return err
	}
	defer pool.Close()

	if err := pool.Ping(ctx); err != nil {
		return err
	}
	if err := db.Migrate(ctx, pool); err != nil {
		return err
	}

	// Optional: without MONGODB_URI the API runs on PostgreSQL alone.
	rec, err := events.New(ctx, os.Getenv("MONGODB_URI"), env("MONGODB_DB", "dropgate"), log)
	if err != nil {
		return err
	}
	defer func() { // runs after Shutdown, so in-flight requests can still log
		cctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := rec.Close(cctx); err != nil {
			log.Error("closing event log", "err", err)
		}
	}()

	srv := &http.Server{
		Addr:              ":" + env("PORT", "8080"),
		Handler:           api.NewRouter(store.New(pool), rec, log),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      20 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		log.Info("listening", "addr", srv.Addr)
		errCh <- srv.ListenAndServe()
	}()

	select {
	case err := <-errCh:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	case <-ctx.Done():
		log.Info("shutting down")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return srv.Shutdown(shutdownCtx)
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
