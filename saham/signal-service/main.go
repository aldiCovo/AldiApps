// Command signal-service sends a market brief for LQ45 stocks to Telegram after each trading
// day, with a screener and the forward paper-trading results of its rules.
// "signal-service backtest" replays the rules on past prices instead.
package main

import (
	"context"
	"encoding/json"
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
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if len(os.Args) > 1 && os.Args[1] == "backtest" {
		if err := runBacktest(ctx, os.Args[2:], os.Stdout); err != nil {
			slog.Error("backtest failed", "error", err)
			os.Exit(1)
		}
		return
	}
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
	if err := serve(ctx); err != nil && !errors.Is(err, http.ErrServerClosed) {
		slog.Error("signal-service stopped", "error", err)
		os.Exit(1)
	}
}

func serve(ctx context.Context) error {
	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		return errors.New("DATABASE_URL is required")
	}
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	runAt, err := parseClock(os.Getenv("RUN_AT"), "17:00")
	if err != nil {
		return err
	}

	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		return fmt.Errorf("opening database: %w", err)
	}
	defer pool.Close()
	if err := migrate(ctx, pool); err != nil {
		return fmt.Errorf("migrating database: %w", err)
	}

	st := &store{pool: pool}
	j := &job{
		store:    st,
		yahoo:    newYahooClient(),
		telegram: newTelegramBot(os.Getenv("TELEGRAM_BOT_TOKEN"), os.Getenv("TELEGRAM_CHAT_ID")),
		tickers:  lq45,
		now:      time.Now,
	}
	// After a restart the gauge starts from the last run, so the staleness alert doesn't fire.
	if _, finished, _, ok, err := st.LastRun(ctx); err != nil {
		return err
	} else if ok {
		jobLastOK.Set(float64(finished.Unix()))
	}

	var background sync.WaitGroup
	defer func() {
		background.Wait()
	}()
	background.Go(func() { j.schedule(ctx, runAt) })

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
	// The last brief, for a look without Telegram.
	mux.HandleFunc("GET /api/latest", func(w http.ResponseWriter, r *http.Request) {
		day, finished, brief, ok, err := st.LastRun(r.Context())
		if err != nil {
			slog.Error("reading the last run", "error", err)
			http.Error(w, "database error", http.StatusInternalServerError)
			return
		}
		if !ok {
			http.Error(w, "no run yet", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"trading_day": day.Format(time.DateOnly), "finished_at": finished, "brief": brief,
		})
	})
	// Runs the job now. force=1 runs even on a day without trading or already done. There is
	// no Service or Ingress, so only someone with kubectl reaches this.
	mux.HandleFunc("POST /run", func(w http.ResponseWriter, r *http.Request) {
		err := j.runAndRecord(r.Context(), r.URL.Query().Get("force") == "1")
		switch {
		case errors.Is(err, errBusy):
			http.Error(w, err.Error(), http.StatusConflict)
		case err != nil:
			http.Error(w, err.Error(), http.StatusInternalServerError)
		default:
			fmt.Fprintln(w, "done")
		}
	})
	server := &http.Server{
		Addr:              ":" + port,
		Handler:           instrument(mux),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		// A run reads 46 price histories and may take a minute or two.
		WriteTimeout: 5 * time.Minute,
		IdleTimeout:  60 * time.Second,
	}
	defer serveMetrics()()

	serveErr := make(chan error, 1)
	go func() {
		slog.Info("listening", "addr", server.Addr, "run_at", runAt.String())
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

// parseClock reads a time of day such as "17:00" into the time since midnight.
func parseClock(value, fallback string) (time.Duration, error) {
	if value == "" {
		value = fallback
	}
	t, err := time.Parse("15:04", value)
	if err != nil {
		return 0, fmt.Errorf("RUN_AT %q isn't a time such as 17:00", value)
	}
	return time.Duration(t.Hour())*time.Hour + time.Duration(t.Minute())*time.Minute, nil
}
