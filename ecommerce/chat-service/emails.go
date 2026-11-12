package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"
)

const (
	// An admin's answer the customer hasn't read this long gets an email.
	replyEmailAfter = "5 minutes"
	// At most one email an hour to the same customer.
	replyEmailEvery  = "1 hour"
	replyEmailCheck  = time.Minute
	replyEmailsAtMax = 50
)

// unreadReply is a conversation whose newest admin answer waits for an email.
type unreadReply struct {
	UserID    int64
	MessageID int64
	Body      string
	Card      *Attachment
}

// UnreadReplies finds conversations whose newest admin answer (not the bot's) is unread, came
// after the last email, is older than replyEmailAfter, and whose last email is older than
// replyEmailEvery.
func (s *Store) UnreadReplies(ctx context.Context) ([]unreadReply, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT c.user_id, m.id, m.body, m.attachment
		 FROM conversations c
		 JOIN LATERAL (
		     SELECT id, body, attachment, created_at FROM messages
		     WHERE user_id = c.user_id AND admin_id > 0
		     ORDER BY id DESC LIMIT 1
		 ) m ON true
		 WHERE m.id > c.customer_read_id AND m.id > c.emailed_up_to
		   AND m.created_at < now() - interval '`+replyEmailAfter+`'
		   AND (c.emailed_at IS NULL OR c.emailed_at < now() - interval '`+replyEmailEvery+`')
		 ORDER BY m.id
		 LIMIT $1`, replyEmailsAtMax)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var list []unreadReply
	for rows.Next() {
		var r unreadReply
		if err := rows.Scan(&r.UserID, &r.MessageID, &r.Body, &r.Card); err != nil {
			return nil, err
		}
		list = append(list, r)
	}
	return list, rows.Err()
}

// MarkEmailed records that an email covered the answers up to messageID.
func (s *Store) MarkEmailed(ctx context.Context, userID, messageID int64) error {
	_, err := s.pool.Exec(ctx,
		"UPDATE conversations SET emailed_up_to = GREATEST(emailed_up_to, $2), emailed_at = now() WHERE user_id = $1",
		userID, messageID)
	return err
}

// replyEmailer asks notification-service to email customers about unread admin answers.
type replyEmailer struct {
	store  *Store
	url    string
	token  string
	client *http.Client
}

func newReplyEmailer(store *Store, url, token string) *replyEmailer {
	return &replyEmailer{store: store, url: url, token: token, client: &http.Client{Timeout: 40 * time.Second}}
}

// Run checks every minute until ctx ends.
func (e *replyEmailer) Run(ctx context.Context) {
	ticker := time.NewTicker(replyEmailCheck)
	defer ticker.Stop()
	for {
		e.sendDue(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (e *replyEmailer) sendDue(ctx context.Context) {
	list, err := e.store.UnreadReplies(ctx)
	if err != nil {
		if ctx.Err() == nil {
			slog.Error("finding unread answers failed", "error", err)
		}
		return
	}
	for _, r := range list {
		preview := r.Body
		if r.Card != nil {
			preview = cardText(r.Card) + "\n" + preview
		}
		status, err := e.ask(ctx, r.UserID, preview)
		switch {
		case err != nil:
			slog.Warn("asking for a reply email failed", "user", r.UserID, "error", err)
			continue
		case status == http.StatusNoContent, status == http.StatusNotFound:
			// Sent, or the account is gone: either way, done with this answer.
		default:
			slog.Warn("notification-service refused a reply email", "user", r.UserID, "status", status)
			continue
		}
		if err := e.store.MarkEmailed(ctx, r.UserID, r.MessageID); err != nil {
			slog.Error("recording a reply email failed", "user", r.UserID, "error", err)
		}
	}
}

func (e *replyEmailer) ask(ctx context.Context, userID int64, preview string) (int, error) {
	body, _ := json.Marshal(map[string]any{"user_id": userID, "preview": shorten(preview, 300)})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.url+"/internal/chat-replies", bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+e.token)
	resp, err := e.client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("notification-service could not be reached: %w", err)
	}
	resp.Body.Close()
	return resp.StatusCode, nil
}
