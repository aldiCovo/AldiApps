// Command notification-service emails customers when their order finishes.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
	if err := run(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		slog.Error("notification-service stopped", "error", err)
		os.Exit(1)
	}
}

func run() error {
	values := map[string]string{}
	for _, name := range []string{"DATABASE_URL", "RABBITMQ_URL", "USER_URL", "USER_INTERNAL_TOKEN", "SMTP_ADDR", "MAIL_FROM"} {
		value := os.Getenv(name)
		if value == "" {
			return fmt.Errorf("%s is required", name)
		}
		values[name] = value
	}
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	mailer, err := newSMTPMailer(values["SMTP_ADDR"], values["MAIL_FROM"])
	if err != nil {
		return err
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

	notifier := &Notifier{
		pool:   pool,
		users:  newUserClient(values["USER_URL"], values["USER_INTERNAL_TOKEN"]),
		mailer: mailer,
	}
	var background sync.WaitGroup
	// The pool must stay open until this has stopped using it.
	defer func() {
		stop()
		background.Wait()
	}()
	background.Go(func() { (&Consumer{notifier: notifier, url: values["RABBITMQ_URL"]}).Run(ctx) })

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		pingCtx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if err := pool.Ping(pingCtx); err != nil {
			slog.Error("database not ready", "error", err)
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	server := &http.Server{
		Addr:              ":" + port,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	serveErr := make(chan error, 1)
	go func() {
		slog.Info("listening", "addr", server.Addr)
		serveErr <- server.ListenAndServe()
	}()
	select {
	case err := <-serveErr:
		return err
	case <-ctx.Done():
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	return server.Shutdown(shutdownCtx)
}
