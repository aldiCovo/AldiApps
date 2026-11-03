// admin-service is the only way in for the shop's admin pages. It lets in accounts whose
// role is admin, asking user-service on every request, and calls the other services'
// internal routes for them. It has no database of its own.
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

const minInternalTokenLength = 32

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
	if err := run(); err != nil {
		slog.Error("admin-service stopped", "error", err)
		os.Exit(1)
	}
}

func run() error {
	values := map[string]string{}
	for _, name := range []string{"USER_URL", "ORDER_URL", "CATALOG_URL", "USER_INTERNAL_TOKEN", "ORDER_INTERNAL_TOKEN", "CATALOG_INTERNAL_TOKEN", "PHOTOS_DIR"} {
		value := os.Getenv(name)
		if value == "" {
			return fmt.Errorf("%s is required", name)
		}
		values[name] = value
	}
	for _, name := range []string{"USER_INTERNAL_TOKEN", "ORDER_INTERNAL_TOKEN", "CATALOG_INTERNAL_TOKEN"} {
		if len(values[name]) < minInternalTokenLength {
			return fmt.Errorf("%s must have at least %d characters", name, minInternalTokenLength)
		}
	}
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	server := &http.Server{
		Addr: ":" + port,
		Handler: newHandler(services{
			userURL:              values["USER_URL"],
			orderURL:             values["ORDER_URL"],
			catalogURL:           values["CATALOG_URL"],
			catalogInternalToken: values["CATALOG_INTERNAL_TOKEN"],
			userInternalToken:    values["USER_INTERNAL_TOKEN"],
			orderInternalToken:   values["ORDER_INTERNAL_TOKEN"],
			photosDir:            values["PHOTOS_DIR"],
		}),
		ReadHeaderTimeout: 5 * time.Second,
		// Long enough to receive a photo over a slow connection.
		ReadTimeout:  60 * time.Second,
		WriteTimeout: 90 * time.Second,
		IdleTimeout:  60 * time.Second,
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
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
