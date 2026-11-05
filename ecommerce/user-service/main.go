// user-service owns the shop's accounts and signs the tokens that prove who a request is from.
package main

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/lock"
)

//go:embed migrations/*.sql
var migrations embed.FS

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
	if err := run(); err != nil {
		slog.Error("user-service stopped", "error", err)
		os.Exit(1)
	}
}

// Long enough that it can only be generated, not guessed.
const minInternalTokenLength = 32

func run() error {
	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		return errors.New("DATABASE_URL is required")
	}
	privateKey := os.Getenv("JWT_PRIVATE_KEY")
	if privateKey == "" {
		return errors.New("JWT_PRIVATE_KEY is required")
	}
	internalToken := os.Getenv("INTERNAL_TOKEN")
	if len(internalToken) < minInternalTokenLength {
		return fmt.Errorf("INTERNAL_TOKEN must have at least %d characters", minInternalTokenLength)
	}
	adminToken := os.Getenv("ADMIN_INTERNAL_TOKEN")
	if len(adminToken) < minInternalTokenLength || adminToken == internalToken {
		return fmt.Errorf("ADMIN_INTERNAL_TOKEN must have at least %d characters and differ from INTERNAL_TOKEN", minInternalTokenLength)
	}
	smtpAddr, mailFrom, shopURL := os.Getenv("SMTP_ADDR"), os.Getenv("MAIL_FROM"), os.Getenv("SHOP_URL")
	if smtpAddr == "" || mailFrom == "" || shopURL == "" {
		return errors.New("SMTP_ADDR, MAIL_FROM and SHOP_URL are required")
	}
	mailer, err := newSMTPMailer(smtpAddr, mailFrom)
	if err != nil {
		return err
	}
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	tokens, err := newTokens(privateKey, tokenLifetime)
	if err != nil {
		return fmt.Errorf("reading JWT_PRIVATE_KEY: %w", err)
	}
	passwords, err := newPasswords(maxConcurrentHashes)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		return fmt.Errorf("opening database: %w", err)
	}
	defer pool.Close()

	if err := migrate(ctx, pool); err != nil {
		return fmt.Errorf("migrating database: %w", err)
	}

	store := &Store{pool: pool}
	resets := newResets(store, passwords, mailer, shopURL)
	// Links still being sent need the pool, which closes after this.
	defer resets.Wait()

	server := &http.Server{
		Addr:              ":" + port,
		Handler:           newHandler(store, tokens, passwords, resets, internalToken, adminToken),
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

func migrate(ctx context.Context, pool *pgxpool.Pool) error {
	db := stdlib.OpenDBFromPool(pool)
	defer db.Close()

	provider, err := newMigrationProvider(db)
	if err != nil {
		return err
	}
	_, err = provider.Up(ctx)
	return err
}

// newMigrationProvider takes a Postgres lock, so two Pods starting during a rollout can't race.
func newMigrationProvider(db *sql.DB) (*goose.Provider, error) {
	files, err := fs.Sub(migrations, "migrations")
	if err != nil {
		return nil, err
	}
	locker, err := lock.NewPostgresSessionLocker()
	if err != nil {
		return nil, err
	}
	return goose.NewProvider(goose.DialectPostgres, db, files, goose.WithSessionLocker(locker))
}
