package main

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/coder/websocket"
)

const (
	// Below the idle limits of Cloudflare and Traefik, so a quiet connection stays open.
	pingInterval = 30 * time.Second
	writeTimeout = 10 * time.Second
	// Browsers send nothing here except control frames.
	readLimit = 1 << 10
)

// tokenChecker is what the handler needs from Verifier.
type tokenChecker interface {
	Check(ctx context.Context, token string) (int64, time.Time, error)
}

func newHandler(hub *Hub, tokens tokenChecker, ready func() bool) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		if !ready() {
			writeError(w, http.StatusServiceUnavailable, "not connected to NATS")
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("GET /ws", func(w http.ResponseWriter, r *http.Request) {
		token, found := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !found || token == "" {
			writeError(w, http.StatusUnauthorized, "a bearer token is required")
			return
		}
		userID, expires, err := tokens.Check(r.Context(), token)
		if errors.Is(err, errKeysUnavailable) {
			slog.Error("checking a token failed", "error", err)
			writeError(w, http.StatusServiceUnavailable, "login checks are not available")
			return
		}
		if err != nil {
			writeError(w, http.StatusUnauthorized, "the token is not valid")
			return
		}
		client, err := hub.Add(userID)
		if errors.Is(err, errUserFull) {
			writeError(w, http.StatusTooManyRequests, err.Error())
			return
		}
		if err != nil {
			writeError(w, http.StatusServiceUnavailable, err.Error())
			return
		}
		defer hub.Remove(userID, client)
		// A token proves the sender, and browsers can't attach one from another site. web
		// checks Origin, because it's the part that reads the cookie.
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
		if err != nil {
			return
		}
		defer conn.CloseNow()
		conn.SetReadLimit(readLimit)
		serve(r.Context(), conn, client, expires)
	})
	return mux
}

func serve(parent context.Context, conn *websocket.Conn, client *Client, expires time.Time) {
	// A token can't be cancelled, but a connection must not outlive it.
	ctx, cancel := context.WithDeadline(conn.CloseRead(parent), expires)
	defer cancel()
	ticker := time.NewTicker(pingInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				conn.Close(websocket.StatusPolicyViolation, "the token expired")
			}
			return
		case <-client.stop:
			conn.Close(websocket.StatusTryAgainLater, "reconnect")
			return
		case payload := <-client.send:
			writeCtx, done := context.WithTimeout(ctx, writeTimeout)
			err := conn.Write(writeCtx, websocket.MessageText, payload)
			done()
			if err != nil {
				return
			}
		case <-ticker.C:
			pingCtx, done := context.WithTimeout(ctx, writeTimeout)
			err := conn.Ping(pingCtx)
			done()
			if err != nil {
				return
			}
		}
	}
}

func writeError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(map[string]string{"error": message}); err != nil {
		slog.Error("writing response failed", "error", err)
	}
}
