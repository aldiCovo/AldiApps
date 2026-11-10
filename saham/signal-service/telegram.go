package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// telegramBot sends the brief through a Telegram bot. Without a token it only logs, so the
// service still runs before the bot exists.
type telegramBot struct {
	token  string
	chatID string
	base   string
	http   *http.Client
}

func newTelegramBot(token, chatID string) *telegramBot {
	return &telegramBot{token: token, chatID: chatID, base: "https://api.telegram.org", http: &http.Client{Timeout: 20 * time.Second}}
}

func (t *telegramBot) Send(ctx context.Context, text string) error {
	if t.token == "" || t.chatID == "" {
		slog.Info("no Telegram bot set, the brief is only logged", "brief", text)
		return nil
	}
	form := url.Values{
		"chat_id":                  {t.chatID},
		"text":                     {text},
		"parse_mode":               {"HTML"},
		"disable_web_page_preview": {"true"},
	}
	var lastErr error
	for attempt := range 3 {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Duration(attempt) * 5 * time.Second):
			}
		}
		lastErr = t.post(ctx, form)
		if lastErr == nil {
			return nil
		}
	}
	return lastErr
}

func (t *telegramBot) post(ctx context.Context, form url.Values) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.base+"/bot"+t.token+"/sendMessage",
		strings.NewReader(form.Encode()))
	if err != nil {
		return errors.New("making the Telegram request failed")
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := t.http.Do(req)
	if err != nil {
		// The error repeats the address, which holds the bot's token, so only its cause is kept.
		var urlErr *url.Error
		if errors.As(err, &urlErr) {
			err = urlErr.Err
		}
		return fmt.Errorf("reaching Telegram: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("telegram answered %d: %.300s", resp.StatusCode, body)
	}
	return nil
}
