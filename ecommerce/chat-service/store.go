package main

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	maxBodyLength = 1000
	// A customer can't write faster than this, so one account can't flood the admins.
	maxCustomerMessagesPerMinute = 20
	defaultPageSize              = 50
	maxPageSize                  = 100
)

var (
	errNotFound = errors.New("conversation not found")
	errTooFast  = errors.New("too many messages, wait a moment")
)

type Store struct {
	pool *pgxpool.Pool
}

// Message is one line in a conversation. From is "customer" or "admin".
type Message struct {
	ID        int64     `json:"id"`
	From      string    `json:"from"`
	Body      string    `json:"body"`
	CreatedAt time.Time `json:"created_at"`
}

// Page is part of a conversation, oldest first.
type Page struct {
	Messages []Message `json:"messages"`
	// Whether older messages exist before the first one here.
	HasMore bool `json:"has_more"`
	// The newest message the other side has seen, for "Dibaca".
	ReadUpTo int64 `json:"read_up_to"`
	// Messages from the other side this side hasn't seen yet.
	Unread int `json:"unread"`
}

// Summary is a conversation in the admins' list.
type Summary struct {
	UserID      int64   `json:"user_id"`
	LastMessage Message `json:"last_message"`
	// The customer wrote last, so the shop owes an answer.
	Waiting bool `json:"waiting"`
	// Customer messages no admin has opened yet.
	Unread int `json:"unread"`
}

type side int

const (
	customerSide side = iota
	adminSide
)

const messageColumns = `id, CASE WHEN admin_id IS NULL THEN 'customer' ELSE 'admin' END, body, created_at`

func scanMessage(row pgx.Row) (Message, error) {
	var m Message
	err := row.Scan(&m.ID, &m.From, &m.Body, &m.CreatedAt)
	return m, err
}

// Send adds a message. adminID is 0 when the customer writes. Admins can only answer a
// conversation the customer started, because only the customer's own token proves the account.
func (s *Store) Send(ctx context.Context, userID, adminID int64, body string) (Message, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Message{}, err
	}
	defer tx.Rollback(ctx)

	if adminID == 0 {
		if _, err := tx.Exec(ctx, "INSERT INTO conversations (user_id) VALUES ($1) ON CONFLICT DO NOTHING", userID); err != nil {
			return Message{}, err
		}
	}
	// Locking the conversation keeps the rate check and last_message_id right when messages
	// arrive at once.
	var exists bool
	if err := tx.QueryRow(ctx, "SELECT true FROM conversations WHERE user_id = $1 FOR UPDATE", userID).Scan(&exists); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Message{}, errNotFound
		}
		return Message{}, err
	}
	if adminID == 0 {
		var recent int
		if err := tx.QueryRow(ctx,
			"SELECT count(*) FROM messages WHERE user_id = $1 AND admin_id IS NULL AND created_at > now() - interval '1 minute'",
			userID).Scan(&recent); err != nil {
			return Message{}, err
		}
		if recent >= maxCustomerMessagesPerMinute {
			return Message{}, errTooFast
		}
	}
	var admin any
	if adminID != 0 {
		admin = adminID
	}
	message, err := scanMessage(tx.QueryRow(ctx,
		"INSERT INTO messages (user_id, admin_id, body) VALUES ($1, $2, $3) RETURNING "+messageColumns,
		userID, admin, body))
	if err != nil {
		return Message{}, err
	}
	// The writer has seen everything up to their own message.
	readColumn := "customer_read_id"
	if adminID != 0 {
		readColumn = "admin_read_id"
	}
	if _, err := tx.Exec(ctx,
		`UPDATE conversations SET last_message_id = $2, last_from_admin = $3, last_at = $4, `+readColumn+` = $2
		 WHERE user_id = $1`,
		userID, message.ID, adminID != 0, message.CreatedAt); err != nil {
		return Message{}, err
	}
	return message, tx.Commit(ctx)
}

// Page returns up to limit messages older than before (0 for the newest), as seen by one side.
// A customer who never wrote gets an empty page, not an error.
func (s *Store) Page(ctx context.Context, userID int64, from side, before int64, limit int) (Page, error) {
	page := Page{Messages: []Message{}}
	var customerRead, adminRead int64
	err := s.pool.QueryRow(ctx, "SELECT customer_read_id, admin_read_id FROM conversations WHERE user_id = $1", userID).
		Scan(&customerRead, &adminRead)
	if errors.Is(err, pgx.ErrNoRows) {
		if from == adminSide {
			return page, errNotFound
		}
		return page, nil
	}
	if err != nil {
		return page, err
	}
	ownRead, otherAuthor := customerRead, "IS NOT NULL"
	page.ReadUpTo = adminRead
	if from == adminSide {
		ownRead, otherAuthor = adminRead, "IS NULL"
		page.ReadUpTo = customerRead
	}
	if err := s.pool.QueryRow(ctx,
		"SELECT count(*) FROM messages WHERE user_id = $1 AND id > $2 AND admin_id "+otherAuthor,
		userID, ownRead).Scan(&page.Unread); err != nil {
		return page, err
	}

	beforeID := before
	if beforeID <= 0 {
		beforeID = 1<<63 - 1
	}
	rows, err := s.pool.Query(ctx,
		"SELECT "+messageColumns+" FROM messages WHERE user_id = $1 AND id < $2 ORDER BY id DESC LIMIT $3",
		userID, beforeID, limit+1)
	if err != nil {
		return page, err
	}
	newestFirst, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Message, error) { return scanMessage(row) })
	if err != nil {
		return page, err
	}
	if len(newestFirst) > limit {
		page.HasMore = true
		newestFirst = newestFirst[:limit]
	}
	for i := len(newestFirst) - 1; i >= 0; i-- {
		page.Messages = append(page.Messages, newestFirst[i])
	}
	return page, nil
}

// MarkRead records that one side has seen every message so far.
func (s *Store) MarkRead(ctx context.Context, userID int64, by side) error {
	column := "customer_read_id"
	if by == adminSide {
		column = "admin_read_id"
	}
	tag, err := s.pool.Exec(ctx, "UPDATE conversations SET "+column+" = last_message_id WHERE user_id = $1", userID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 && by == adminSide {
		return errNotFound
	}
	return nil
}

// CustomerUnread counts the shop's messages the customer hasn't seen.
func (s *Store) CustomerUnread(ctx context.Context, userID int64) (int, error) {
	var unread int
	err := s.pool.QueryRow(ctx,
		`SELECT count(*) FROM messages m JOIN conversations c USING (user_id)
		 WHERE m.user_id = $1 AND m.admin_id IS NOT NULL AND m.id > c.customer_read_id`,
		userID).Scan(&unread)
	return unread, err
}

// Waiting counts the conversations where the customer wrote last.
func (s *Store) Waiting(ctx context.Context) (int, error) {
	var waiting int
	err := s.pool.QueryRow(ctx,
		"SELECT count(*) FROM conversations WHERE last_message_id > 0 AND NOT last_from_admin").Scan(&waiting)
	return waiting, err
}

// Conversations lists the newest conversations first, only those waiting for an answer when
// waitingOnly is set, and how many match in all.
func (s *Store) Conversations(ctx context.Context, waitingOnly bool, limit, offset int) ([]Summary, int, error) {
	filter := "c.last_message_id > 0"
	if waitingOnly {
		filter += " AND NOT c.last_from_admin"
	}
	var total int
	if err := s.pool.QueryRow(ctx, "SELECT count(*) FROM conversations c WHERE "+filter).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := s.pool.Query(ctx,
		`SELECT c.user_id, NOT c.last_from_admin,
		        (SELECT count(*) FROM messages u WHERE u.user_id = c.user_id AND u.admin_id IS NULL AND u.id > c.admin_read_id),
		        m.id, CASE WHEN m.admin_id IS NULL THEN 'customer' ELSE 'admin' END, m.body, m.created_at
		 FROM conversations c JOIN messages m ON m.id = c.last_message_id
		 WHERE `+filter+`
		 ORDER BY c.last_at DESC, c.user_id DESC LIMIT $1 OFFSET $2`,
		limit, offset)
	if err != nil {
		return nil, 0, err
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Summary, error) {
		var s Summary
		err := row.Scan(&s.UserID, &s.Waiting, &s.Unread, &s.LastMessage.ID, &s.LastMessage.From, &s.LastMessage.Body, &s.LastMessage.CreatedAt)
		return s, err
	})
	if list == nil {
		list = []Summary{}
	}
	return list, total, err
}
