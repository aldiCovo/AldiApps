package main

import (
	"errors"
	"net/http"
	"strings"
	"time"
)

type botSettingsView struct {
	BotSettings
	// Which AIs have a key. Without the chosen one's, messages the menu doesn't answer go to
	// the admins.
	GeminiConfigured bool `json:"gemini_configured"`
	ClaudeConfigured bool `json:"claude_configured"`
	// Whether Telegram has a bot token and chat.
	TelegramConfigured bool `json:"telegram_configured"`
	botUse
	// The bot answers a new message right now.
	AnsweringNow bool `json:"answering_now"`
}

func addBotRoutes(mux *http.ServeMux, store *Store, bot *Assistant,
	authenticated func(func(http.ResponseWriter, *http.Request, int64)) http.HandlerFunc,
	internal func(http.HandlerFunc) http.HandlerFunc,
	customerID func(http.ResponseWriter, *http.Request) (int64, bool)) {

	// "Bicara dengan admin": the customer asks for a person.
	mux.HandleFunc("POST /chat/handoff", authenticated(func(w http.ResponseWriter, r *http.Request, userID int64) {
		bearer, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		started, err := store.Handoff(r.Context(), userID, reasonCustomer)
		if err != nil {
			writeFailure(w, err)
			return
		}
		if started {
			// The note goes first, so the admins see the conversation with it.
			bot.say(r.Context(), userID, botAskedAdmin, nil)
			bot.tell(r.Context(), userID, bearer, reasonCustomer)
		}
		w.WriteHeader(http.StatusNoContent)
	}))

	mux.HandleFunc("GET /internal/bot-settings", internal(func(w http.ResponseWriter, r *http.Request) {
		settings, err := store.BotSettings(r.Context())
		if err != nil {
			writeFailure(w, err)
			return
		}
		now := bot.now().In(jakarta)
		use, err := store.BotUse(r.Context(), now, time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, jakarta))
		if err != nil {
			writeFailure(w, err)
			return
		}
		writeJSON(w, http.StatusOK, botSettingsView{
			BotSettings: settings, GeminiConfigured: bot.gemini != nil, ClaudeConfigured: bot.claude != nil,
			TelegramConfigured: bot.telegram != nil, botUse: use, AnsweringNow: bot.answersNow(settings),
		})
	}))
	mux.HandleFunc("PUT /internal/bot-settings", internal(func(w http.ResponseWriter, r *http.Request) {
		var in BotSettings
		if !readJSON(w, r, &in) {
			return
		}
		in.StoreInfo = strings.TrimSpace(strings.ReplaceAll(in.StoreInfo, "\r\n", "\n"))
		if err := in.validate(); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		if err := store.SaveBotSettings(r.Context(), in); err != nil {
			writeFailure(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))

	// An admin takes a conversation over ("on": false) or gives it back to the bot.
	mux.HandleFunc("POST /internal/conversations/{id}/bot", internal(func(w http.ResponseWriter, r *http.Request) {
		userID, ok := customerID(w, r)
		if !ok {
			return
		}
		var in struct {
			On *bool `json:"on"`
		}
		if !readJSON(w, r, &in) {
			return
		}
		if in.On == nil {
			writeError(w, http.StatusBadRequest, "on is required")
			return
		}
		err := store.SetBotOff(r.Context(), userID, !*in.On)
		if errors.Is(err, errNotFound) {
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
		if err != nil {
			writeFailure(w, err)
			return
		}
		// The customer's header changes between the bot and the admins.
		bot.signals.Message(userID, 0)
		w.WriteHeader(http.StatusNoContent)
	}))
}
