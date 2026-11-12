package main

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"
)

const maxChatPreview = 300

// addChatRoutes lets chat-service ask for the email that tells a customer an admin answered.
// chat-service decides when: after the answer stayed unread a while, at most once an hour.
func addChatRoutes(mux *http.ServeMux, n *Notifier, token, shopURL string) {
	mux.HandleFunc("POST /internal/chat-replies", func(w http.ResponseWriter, r *http.Request) {
		given, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if subtle.ConstantTimeCompare([]byte(given), []byte(token)) != 1 {
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
			return
		}
		var in struct {
			UserID  int64  `json:"user_id"`
			Preview string `json:"preview"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 8<<10)).Decode(&in); err != nil || in.UserID <= 0 || !utf8.ValidString(in.Preview) {
			http.Error(w, `{"error":"user_id and preview are required"}`, http.StatusBadRequest)
			return
		}
		user, err := n.users.Find(r.Context(), in.UserID)
		if errors.Is(err, errUserGone) {
			http.Error(w, `{"error":"no such user"}`, http.StatusNotFound)
			return
		}
		if err != nil {
			slog.Error("reading the customer for a chat email failed", "user_id", in.UserID, "error", err)
			http.Error(w, `{"error":"user-service did not answer"}`, http.StatusBadGateway)
			return
		}
		email := composeChatReply(user, in.UserID, in.Preview, shopURL, time.Now())
		if err := n.mailer.Send(email); err != nil {
			slog.Error("the mail server refused a chat email", "user_id", in.UserID, "error", err)
			http.Error(w, `{"error":"the mail server refused the email"}`, http.StatusBadGateway)
			return
		}
		slog.Info("sent an email", "user_id", in.UserID, "kind", "chat_reply")
		w.WriteHeader(http.StatusNoContent)
	})
}

func composeChatReply(user User, userID int64, preview, shopURL string, now time.Time) Email {
	preview = strings.TrimSpace(preview)
	if utf8.RuneCountInString(preview) > maxChatPreview {
		preview = string([]rune(preview)[:maxChatPreview]) + "…"
	}
	var body strings.Builder
	fmt.Fprintf(&body, "Halo %s,\r\n\r\nAdmin Prastika membalas chat kamu:\r\n\r\n", user.Name)
	if preview != "" {
		for _, line := range strings.Split(strings.ReplaceAll(preview, "\r\n", "\n"), "\n") {
			fmt.Fprintf(&body, "> %s\r\n", line)
		}
	} else {
		body.WriteString("> (mengirim lampiran)\r\n")
	}
	fmt.Fprintf(&body, "\r\nBaca dan balas di sini:\r\n%s/#chat\r\n\r\n", strings.TrimRight(shopURL, "/"))
	body.WriteString("Email ini dikirim karena ada balasan admin yang belum kamu baca. Paling banyak satu email per jam.\r\n")
	return Email{
		To:        user,
		Kind:      "chat_reply",
		Subject:   "Admin Prastika membalas chat kamu",
		Body:      body.String(),
		MessageID: fmt.Sprintf("chat-%d-%d", userID, now.Unix()),
	}
}
