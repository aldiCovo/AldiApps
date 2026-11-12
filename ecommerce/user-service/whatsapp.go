package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const fonnteURL = "https://api.fonnte.com/send"

// Fonnte sends WhatsApp messages from the shop's own number, which is linked to Fonnte by
// scanning a QR code in its dashboard.
type Fonnte struct {
	url    string
	token  string
	client *http.Client
}

func newFonnte(token string) *Fonnte {
	return &Fonnte{url: fonnteURL, token: token, client: &http.Client{Timeout: 20 * time.Second}}
}

func (f *Fonnte) Send(ctx context.Context, phone, text string) error {
	form := url.Values{
		"target":      {phone},
		"message":     {text},
		"countryCode": {"62"},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, f.url, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", f.token)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := f.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	var answer struct {
		Status bool   `json:"status"`
		Reason string `json:"reason"`
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if err != nil {
		return err
	}
	// Fonnte answers 200 with status false for a number that has no WhatsApp, among others.
	if err := json.Unmarshal(data, &answer); err != nil || resp.StatusCode != http.StatusOK || !answer.Status {
		return fmt.Errorf("fonnte answered %d: %q", resp.StatusCode, answer.Reason)
	}
	return nil
}
