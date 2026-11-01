// Command realtime-service tells browsers when an order changes, over WebSockets.
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
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
	if err := run(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		slog.Error("stopped", "error", err)
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
	natsURL, err := requireEnv("NATS_URL")
	if err != nil {
		return err
	}
	userURL, err := requireEnv("USER_URL")
	if err != nil {
		return err
	}
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	hub := newHub()
	conn, err := listen(natsURL, hub)
	if err != nil {
		return fmt.Errorf("connecting to NATS: %w", err)
	}
	defer conn.Close()

	server := &http.Server{
		Addr:              ":" + port,
		Handler:           newHandler(hub, newVerifier(userURL), conn.IsConnected),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      20 * time.Second,
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
	err = server.Shutdown(shutdownCtx)
	hub.CloseAll()
	ended := make(chan struct{})
	go func() {
		hub.Wait()
		close(ended)
	}()
	select {
	case <-ended:
	case <-shutdownCtx.Done():
		slog.Warn("stopped before every connection ended")
	}
	return err
}
