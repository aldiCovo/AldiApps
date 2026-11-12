package main

import (
	"context"
	"errors"
	"strings"
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

// Message is one line in a conversation. From is "customer", "bot" or "admin".
type Message struct {
	ID   int64  `json:"id"`
	From string `json:"from"`
	// Empty when the message is only a card.
	Body       string      `json:"body"`
	Attachment *Attachment `json:"attachment,omitempty"`
	CreatedAt  time.Time   `json:"created_at"`
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
	// A person must answer: the bot handed the conversation over, or the customer asked.
	NeedsAdmin bool `json:"needs_admin"`
	// The bot is writing an answer now.
	BotWorking bool `json:"bot_working"`
	// The bot will answer the customer's next message.
	BotOn bool `json:"bot_on"`
	// Admins only: an admin has taken the conversation over, so the bot stays quiet.
	BotOff bool `json:"bot_off"`
}

// Summary is a conversation in the admins' list.
type Summary struct {
	UserID      int64   `json:"user_id"`
	LastMessage Message `json:"last_message"`
	// A person must answer.
	Waiting bool `json:"waiting"`
	// Why, as the bot or the customer gave it.
	Reason string `json:"reason,omitempty"`
	// An admin has taken the conversation over, so the bot stays quiet.
	BotOff bool `json:"bot_off"`
	// Customer messages no admin has opened yet.
	Unread int `json:"unread"`
}

type side int

const (
	customerSide side = iota
	adminSide
)

// botWriter is what Send takes for the shop's assistant, whose messages have admin_id 0.
const botWriter int64 = -1

// fromColumn names who wrote a row of messages: no admin is the customer, admin 0 the bot.
const fromColumn = `CASE WHEN admin_id IS NULL THEN 'customer' WHEN admin_id = 0 THEN 'bot' ELSE 'admin' END`

const messageColumns = `id, ` + fromColumn + `, body, attachment, created_at`

func scanMessage(row pgx.Row) (Message, error) {
	var m Message
	err := row.Scan(&m.ID, &m.From, &m.Body, &m.Attachment, &m.CreatedAt)
	return m, err
}

// Send adds a message, with a card when attachment isn't nil. adminID is 0 when the customer
// writes and botWriter when the bot does. The shop can only answer a conversation the customer
// started, because only the customer's own token proves the account. An admin's answer settles
// what the customer needed and keeps the bot quiet for botQuietAfterAdmin.
func (s *Store) Send(ctx context.Context, userID, adminID int64, body string, attachment *Attachment) (Message, error) {
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
	var admin, card any
	switch adminID {
	case 0:
	case botWriter:
		admin = 0
	default:
		admin = adminID
	}
	if attachment != nil {
		card = attachment
	}
	message, err := scanMessage(tx.QueryRow(ctx,
		"INSERT INTO messages (user_id, admin_id, body, attachment) VALUES ($1, $2, $3, $4) RETURNING "+messageColumns,
		userID, admin, body, card))
	if err != nil {
		return Message{}, err
	}
	// The writer has seen everything up to their own message. The bot reading isn't an admin
	// reading.
	set := ", customer_read_id = $2"
	switch adminID {
	case 0:
	case botWriter:
		set = ""
	default:
		set = ", admin_read_id = $2, needs_admin = false, needs_reason = '', " +
			"bot_off_until = GREATEST(bot_off_until, now() + interval '" + botQuietAfterAdmin + "')"
	}
	if _, err := tx.Exec(ctx,
		`UPDATE conversations SET last_message_id = $2, last_from_admin = $3, last_at = $4`+set+`
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
	err := s.pool.QueryRow(ctx,
		`SELECT customer_read_id, admin_read_id, needs_admin, bot_busy, COALESCE(bot_off_until > now(), false)
		 FROM conversations WHERE user_id = $1`, userID).
		Scan(&customerRead, &adminRead, &page.NeedsAdmin, &page.BotWorking, &page.BotOff)
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

// Waiting counts the conversations a person must answer.
func (s *Store) Waiting(ctx context.Context) (int, error) {
	var waiting int
	err := s.pool.QueryRow(ctx, "SELECT count(*) FROM conversations WHERE needs_admin").Scan(&waiting)
	return waiting, err
}

// Conversation filters for the admins' list.
const (
	filterAll     = "all"
	filterWaiting = "waiting"
	// Those the bot is handling, with nothing for a person to do.
	filterBot = "bot"
)

// Conversations lists the newest conversations first, filtered, and how many match in all.
func (s *Store) Conversations(ctx context.Context, filterName string, limit, offset int) ([]Summary, int, error) {
	filter := "c.last_message_id > 0"
	switch filterName {
	case filterWaiting:
		filter += " AND c.needs_admin"
	case filterBot:
		filter += " AND NOT c.needs_admin AND EXISTS (SELECT 1 FROM messages b WHERE b.user_id = c.user_id AND b.admin_id = 0)"
	}
	var total int
	if err := s.pool.QueryRow(ctx, "SELECT count(*) FROM conversations c WHERE "+filter).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := s.pool.Query(ctx,
		`SELECT c.user_id, c.needs_admin, c.needs_reason, COALESCE(c.bot_off_until > now(), false),
		        (SELECT count(*) FROM messages u WHERE u.user_id = c.user_id AND u.admin_id IS NULL AND u.id > c.admin_read_id),
		        m.id, `+strings.ReplaceAll(fromColumn, "admin_id", "m.admin_id")+`, m.body, m.attachment, m.created_at
		 FROM conversations c JOIN messages m ON m.id = c.last_message_id
		 WHERE `+filter+`
		 ORDER BY c.last_at DESC, c.user_id DESC LIMIT $1 OFFSET $2`,
		limit, offset)
	if err != nil {
		return nil, 0, err
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Summary, error) {
		var s Summary
		err := row.Scan(&s.UserID, &s.Waiting, &s.Reason, &s.BotOff, &s.Unread, &s.LastMessage.ID, &s.LastMessage.From, &s.LastMessage.Body, &s.LastMessage.Attachment, &s.LastMessage.CreatedAt)
		return s, err
	})
	if list == nil {
		list = []Summary{}
	}
	return list, total, err
}
