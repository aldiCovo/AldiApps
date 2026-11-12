// chat-service keeps the conversations between customers and the shop's admins. Customers
// reach it through web with their own token, admins through admin-service with the internal
// token, and realtime-service tells the customer's browser when the shop answers.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

const minInternalTokenLength = 32

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
	if err := run(); err != nil {
		slog.Error("chat-service stopped", "error", err)
		os.Exit(1)
	}
}

func run() error {
	values := map[string]string{}
	for _, name := range []string{"DATABASE_URL", "USER_URL", "NATS_URL", "INTERNAL_TOKEN"} {
		value := os.Getenv(name)
		if value == "" {
			return fmt.Errorf("%s is required", name)
		}
		values[name] = value
	}
	if len(values["INTERNAL_TOKEN"]) < minInternalTokenLength {
		return fmt.Errorf("INTERNAL_TOKEN must have at least %d characters", minInternalTokenLength)
	}
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	pool, err := pgxpool.New(ctx, values["DATABASE_URL"])
	if err != nil {
		return fmt.Errorf("opening database: %w", err)
	}
	defer pool.Close()
	if err := migrate(ctx, pool); err != nil {
		return fmt.Errorf("migrating database: %w", err)
	}
	notifier, err := newNotifier(values["NATS_URL"])
	if err != nil {
		return fmt.Errorf("connecting to NATS: %w", err)
	}
	defer notifier.Close()

	server := &http.Server{
		Addr:              ":" + port,
		Handler:           newHandler(&Store{pool: pool}, newVerifier(values["USER_URL"]), notifier, values["INTERNAL_TOKEN"]),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      20 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	server.Handler = instrument(server.Handler)
	defer serveMetrics()()

	serveErr := make(chan error, 1)
	go func() {
		slog.Info("listening", "addr", server.Addr)
		serveErr <- server.ListenAndServe()
	}()
	select {
	case err := <-serveErr:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	return server.Shutdown(shutdownCtx)
}
