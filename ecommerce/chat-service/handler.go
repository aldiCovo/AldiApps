package main

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const maxBodyBytes = 1 << 14

// signaler is what the handler needs from Notifier, so tests can do without NATS.
type signaler interface {
	Message(userID, messageID int64)
}

type messageInput struct {
	Body   string       `json:"body"`
	Attach *attachInput `json:"attach"`
}

type adminMessageInput struct {
	Body   string       `json:"body"`
	Attach *attachInput `json:"attach"`
	// The admin writing, as admin-service checked it.
	AdminID int64 `json:"admin_id"`
}

// cleanBody trims the text and keeps line breaks as \n, so both sides show the same thing.
// The text may be empty only when a card goes with it.
func cleanBody(body string, withCard bool) (string, error) {
	body = strings.TrimSpace(strings.ReplaceAll(body, "\r\n", "\n"))
	switch {
	case body == "" && !withCard:
		return "", errors.New("body is required")
	case !utf8.ValidString(body) || strings.ContainsRune(body, 0):
		return "", errors.New("body must be text")
	case utf8.RuneCountInString(body) > maxBodyLength:
		return "", fmt.Errorf("body must be at most %d characters", maxBodyLength)
	}
	return body, nil
}

// pageArgs reads ?before= and ?limit=.
func pageArgs(r *http.Request) (before int64, limit int, err error) {
	limit = defaultPageSize
	if value := r.URL.Query().Get("limit"); value != "" {
		limit, err = strconv.Atoi(value)
		if err != nil || limit < 1 || limit > maxPageSize {
			return 0, 0, fmt.Errorf("limit must be between 1 and %d", maxPageSize)
		}
	}
	if value := r.URL.Query().Get("before"); value != "" {
		before, err = strconv.ParseInt(value, 10, 64)
		if err != nil || before < 1 {
			return 0, 0, errors.New("before must be a message id")
		}
	}
	return before, limit, nil
}

func newHandler(store *Store, verifier *Verifier, signals signaler, cards attacher, bot *Assistant, internalToken string) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if err := store.pool.Ping(ctx); err != nil {
			slog.Error("database not ready", "error", err)
			writeError(w, http.StatusServiceUnavailable, "database not ready")
			return
		}
		w.WriteHeader(http.StatusOK)
	})

	authenticated := func(next func(http.ResponseWriter, *http.Request, int64)) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			token, found := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
			if !found {
				writeUnauthorized(w, "a bearer token is required")
				return
			}
			userID, err := verifier.UserID(r.Context(), token)
			if errors.Is(err, errKeysUnavailable) {
				slog.Error("checking a token failed", "error", err)
				writeError(w, http.StatusServiceUnavailable, "login checks are not available")
				return
			}
			if err != nil {
				writeUnauthorized(w, "the token is not valid")
				return
			}
			w.Header().Set("Cache-Control", "no-store")
			next(w, r, userID)
		}
	}

	internal := func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			bearer, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
			if subtle.ConstantTimeCompare([]byte(bearer), []byte(internalToken)) != 1 {
				writeUnauthorized(w, "the internal token is required")
				return
			}
			w.Header().Set("Cache-Control", "no-store")
			next(w, r)
		}
	}

	customerID := func(w http.ResponseWriter, r *http.Request) (int64, bool) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil || id <= 0 {
			writeError(w, http.StatusNotFound, errNotFound.Error())
			return 0, false
		}
		return id, true
	}

	writePage := func(w http.ResponseWriter, r *http.Request, userID int64, from side) {
		before, limit, err := pageArgs(r)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		page, err := store.Page(r.Context(), userID, from, before, limit)
		if errors.Is(err, errNotFound) {
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
		if err != nil {
			writeFailure(w, err)
			return
		}
		settings, err := store.BotSettings(r.Context())
		if err != nil {
			writeFailure(w, err)
			return
		}
		page.BotOn = bot.answersNow(settings) && !page.NeedsAdmin && !page.BotOff
		page.MenuOn = bot.answersNow(settings) && !page.BotOff
		writeJSON(w, http.StatusOK, page)
	}

	// card builds what in asks to attach. Customers may attach a product or one of their own
	// orders; admins only a product, since only the customer's token can prove an order is theirs.
	card := func(w http.ResponseWriter, r *http.Request, in *attachInput, bearer string) (*Attachment, bool) {
		if in == nil {
			return nil, true
		}
		if in.ID <= 0 {
			writeError(w, http.StatusBadRequest, "attach.id must be a positive integer")
			return nil, false
		}
		var attachment *Attachment
		var err error
		switch {
		case in.Kind == attachProduct:
			attachment, err = cards.Product(r.Context(), in.ID)
		case in.Kind == attachOrder && bearer != "":
			attachment, err = cards.Order(r.Context(), in.ID, bearer)
		case in.Kind == attachOrder:
			writeError(w, http.StatusBadRequest, "only the customer can attach an order")
			return nil, false
		default:
			writeError(w, http.StatusBadRequest, `attach.kind must be "product" or "order"`)
			return nil, false
		}
		switch {
		case errors.Is(err, errCannotAttach):
			writeError(w, http.StatusUnprocessableEntity, err.Error())
			return nil, false
		case err != nil:
			slog.Error("building a card failed", "error", err)
			writeError(w, http.StatusBadGateway, errLookupFailed.Error())
			return nil, false
		}
		return attachment, true
	}

	send := func(w http.ResponseWriter, r *http.Request, userID, adminID int64, body string, in *attachInput, bearer string) bool {
		body, err := cleanBody(body, in != nil)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return false
		}
		attachment, ok := card(w, r, in, bearer)
		if !ok {
			return false
		}
		message, err := store.Send(r.Context(), userID, adminID, body, attachment)
		switch {
		case errors.Is(err, errNotFound):
			writeError(w, http.StatusNotFound, err.Error())
			return false
		case errors.Is(err, errTooFast):
			w.Header().Set("Retry-After", "60")
			writeError(w, http.StatusTooManyRequests, err.Error())
			return false
		case err != nil:
			writeFailure(w, err)
			return false
		}
		// Every tab of the customer hears of it, including the one that sent it.
		signals.Message(userID, message.ID)
		writeJSON(w, http.StatusCreated, map[string]Message{"message": message})
		return true
	}

	// The customer's own conversation.
	mux.HandleFunc("GET /chat", authenticated(func(w http.ResponseWriter, r *http.Request, userID int64) {
		writePage(w, r, userID, customerSide)
	}))
	mux.HandleFunc("GET /chat/unread", authenticated(func(w http.ResponseWriter, r *http.Request, userID int64) {
		unread, err := store.CustomerUnread(r.Context(), userID)
		if err != nil {
			writeFailure(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]int{"unread": unread})
	}))
	mux.HandleFunc("POST /chat/messages", authenticated(func(w http.ResponseWriter, r *http.Request, userID int64) {
		var in messageInput
		if readJSON(w, r, &in) {
			bearer, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
			if send(w, r, userID, 0, in.Body, in.Attach, bearer) {
				bot.CustomerWrote(userID, bearer)
			}
		}
	}))
	mux.HandleFunc("POST /chat/read", authenticated(func(w http.ResponseWriter, r *http.Request, userID int64) {
		if err := store.MarkRead(r.Context(), userID, customerSide); err != nil {
			writeFailure(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))

	// For admin-service, which has checked that the caller is an admin.
	mux.HandleFunc("GET /internal/conversations", internal(func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query()
		filter := query.Get("filter")
		if filter != "" && filter != filterWaiting && filter != filterAll && filter != filterBot {
			writeError(w, http.StatusBadRequest, `filter must be "waiting", "bot" or "all"`)
			return
		}
		limit, offset := 20, 0
		var err error
		if value := query.Get("limit"); value != "" {
			if limit, err = strconv.Atoi(value); err != nil || limit < 1 || limit > maxPageSize {
				writeError(w, http.StatusBadRequest, fmt.Sprintf("limit must be between 1 and %d", maxPageSize))
				return
			}
		}
		if value := query.Get("offset"); value != "" {
			if offset, err = strconv.Atoi(value); err != nil || offset < 0 {
				writeError(w, http.StatusBadRequest, "offset must be 0 or more")
				return
			}
		}
		list, total, err := store.Conversations(r.Context(), filter, limit, offset)
		if err != nil {
			writeFailure(w, err)
			return
		}
		waiting, err := store.Waiting(r.Context())
		if err != nil {
			writeFailure(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"conversations": list, "total": total, "waiting": waiting})
	}))
	mux.HandleFunc("GET /internal/chat/waiting", internal(func(w http.ResponseWriter, r *http.Request) {
		waiting, err := store.Waiting(r.Context())
		if err != nil {
			writeFailure(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]int{"waiting": waiting})
	}))
	mux.HandleFunc("GET /internal/conversations/{id}/messages", internal(func(w http.ResponseWriter, r *http.Request) {
		if userID, ok := customerID(w, r); ok {
			writePage(w, r, userID, adminSide)
		}
	}))
	mux.HandleFunc("POST /internal/conversations/{id}/messages", internal(func(w http.ResponseWriter, r *http.Request) {
		userID, ok := customerID(w, r)
		if !ok {
			return
		}
		var in adminMessageInput
		if !readJSON(w, r, &in) {
			return
		}
		if in.AdminID <= 0 {
			writeError(w, http.StatusBadRequest, "admin_id is required")
			return
		}
		send(w, r, userID, in.AdminID, in.Body, in.Attach, "")
	}))
	mux.HandleFunc("POST /internal/conversations/{id}/read", internal(func(w http.ResponseWriter, r *http.Request) {
		userID, ok := customerID(w, r)
		if !ok {
			return
		}
		err := store.MarkRead(r.Context(), userID, adminSide)
		if errors.Is(err, errNotFound) {
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
		if err != nil {
			writeFailure(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))

	addQuickRoutes(mux, store, internal)
	addBotRoutes(mux, store, bot, authenticated, internal, customerID)
	addMenuRoutes(mux, store, bot, authenticated)

	return mux
}

func readJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeError(w, http.StatusRequestEntityTooLarge, "request body is too large")
			return false
		}
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return false
	}
	return true
}

func writeFailure(w http.ResponseWriter, err error) {
	slog.Error("request failed", "error", err)
	writeError(w, http.StatusInternalServerError, "internal server error")
}

func writeUnauthorized(w http.ResponseWriter, message string) {
	w.Header().Set("WWW-Authenticate", "Bearer")
	writeError(w, http.StatusUnauthorized, message)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(body); err != nil {
		slog.Error("writing response failed", "error", err)
	}
}
