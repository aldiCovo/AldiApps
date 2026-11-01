// search-service keeps a search index of the catalog's products and answers searches from it.
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
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
	if err := run(); err != nil {
		slog.Error("search-service stopped", "error", err)
		os.Exit(1)
	}
}

func requiredEnv(names ...string) (map[string]string, error) {
	values := make(map[string]string, len(names))
	for _, name := range names {
		value := os.Getenv(name)
		if value == "" {
			return nil, errors.New(name + " is required")
		}
		values[name] = value
	}
	return values, nil
}

func run() error {
	env, err := requiredEnv("ELASTICSEARCH_URL", "RABBITMQ_URL", "CATALOG_URL")
	if err != nil {
		return err
	}
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	elastic := newElastic(env["ELASTICSEARCH_URL"], productIndex)
	indexer := &Indexer{elastic: elastic, rabbitmqURL: env["RABBITMQ_URL"], catalogURL: env["CATALOG_URL"]}
	indexerDone := make(chan struct{})
	go func() {
		defer close(indexerDone)
		indexer.Run(ctx)
	}()
	defer func() {
		stop()
		<-indexerDone
	}()

	server := &http.Server{
		Addr:              ":" + port,
		Handler:           newHandler(elastic),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      15 * time.Second,
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
