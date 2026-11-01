// order-service owns the shop's orders and leads the saga that reserves stock and takes payment.
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

const (
	defaultPaymentWindow = 15 * time.Minute
	expiryCheckInterval  = 5 * time.Second
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
	if err := run(); err != nil {
		slog.Error("order-service stopped", "error", err)
		os.Exit(1)
	}
}

func requireEnv(name string) (string, error) {
	value := os.Getenv(name)
	if value == "" {
		return "", fmt.Errorf("%s is required", name)
	}
	return value, nil
}

func run() error {
	values := map[string]string{}
	for _, name := range []string{"DATABASE_URL", "RABBITMQ_URL", "NATS_URL", "CATALOG_URL", "USER_URL"} {
		value, err := requireEnv(name)
		if err != nil {
			return err
		}
		values[name] = value
	}
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	paymentWindow := defaultPaymentWindow
	if value := os.Getenv("PAYMENT_WINDOW"); value != "" {
		var err error
		if paymentWindow, err = time.ParseDuration(value); err != nil || paymentWindow <= 0 {
			return errors.New("PAYMENT_WINDOW must be a positive duration like 15m")
		}
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

	store := &Store{pool: pool, paymentWindow: paymentWindow, notify: notifier.Changed}
	var background sync.WaitGroup
	// The pool must stay open until these have stopped using it.
	defer func() {
		stop()
		background.Wait()
	}()
	for _, task := range []func(context.Context){
		(&Publisher{pool: pool, url: values["RABBITMQ_URL"]}).Run,
		(&Consumer{store: store, url: values["RABBITMQ_URL"]}).Run,
		func(ctx context.Context) { store.RunExpiry(ctx, expiryCheckInterval) },
	} {
		background.Go(func() { task(ctx) })
	}

	server := &http.Server{
		Addr:              ":" + port,
		Handler:           newHandler(store, newVerifier(values["USER_URL"]), newCatalog(values["CATALOG_URL"])),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      30 * time.Second,
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
