package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// Telegram tells the admins' group or chat that a conversation needs a person. It only
// tells: answers still go from the admin pages, so the customer stays in the shop's chat.
type Telegram struct {
	baseURL string
	token   string
	chatID  string
	client  *http.Client
}

func newTelegram(baseURL, token, chatID string) *Telegram {
	return &Telegram{baseURL: baseURL, token: token, chatID: chatID, client: &http.Client{Timeout: 10 * time.Second}}
}

// Send posts plain text, so nothing in a customer's words is read as formatting.
func (t *Telegram) Send(ctx context.Context, text string, silent bool) error {
	body, err := json.Marshal(map[string]any{
		"chat_id":              t.chatID,
		"text":                 text,
		"disable_notification": silent,
		"link_preview_options": map[string]bool{"is_disabled": true},
	})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.baseURL+"/bot"+t.token+"/sendMessage", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := t.client.Do(req)
	if err != nil {
		// The error names the URL, which holds the token.
		return fmt.Errorf("telegram could not be reached")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("telegram answered %d", resp.StatusCode)
	}
	return nil
}
