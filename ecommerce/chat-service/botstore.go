package main

import (
	"context"
	"errors"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

const (
	// After an admin answers, the bot leaves the conversation to people this long.
	botQuietAfterAdmin = "12 hours"
	maxStoreInfoLength = 4000

	botAlways       = "always"
	botOutsideHours = "outside_hours"
	botOff          = "off"
)

// BotSettings is what the admins choose on the settings page.
type BotSettings struct {
	Mode string `json:"mode"`
	// What the bot can't read from the shop's systems: returns, sizes, materials.
	StoreInfo string `json:"store_info"`
	// Tell the admins on Telegram when a conversation starts needing them.
	Telegram bool `json:"telegram"`
	// Without a sound outside working hours.
	QuietOutsideHours bool `json:"quiet_outside_hours"`
}

func (s BotSettings) validate() error {
	switch {
	case s.Mode != botAlways && s.Mode != botOutsideHours && s.Mode != botOff:
		return errors.New(`mode must be "always", "outside_hours" or "off"`)
	case !utf8.ValidString(s.StoreInfo):
		return errors.New("store_info must be text")
	case utf8.RuneCountInString(s.StoreInfo) > maxStoreInfoLength:
		return errors.New("store_info must be at most 4000 characters")
	}
	return nil
}

func (s *Store) BotSettings(ctx context.Context) (BotSettings, error) {
	var b BotSettings
	err := s.pool.QueryRow(ctx, "SELECT mode, store_info, telegram, quiet_outside_hours FROM bot_settings WHERE id = 1").
		Scan(&b.Mode, &b.StoreInfo, &b.Telegram, &b.QuietOutsideHours)
	return b, err
}

func (s *Store) SaveBotSettings(ctx context.Context, b BotSettings) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE bot_settings SET mode = $1, store_info = $2, telegram = $3, quiet_outside_hours = $4, updated_at = now()
		 WHERE id = 1`, b.Mode, b.StoreInfo, b.Telegram, b.QuietOutsideHours)
	return err
}

// botState is where a conversation stands for the bot.
type botState struct {
	NeedsAdmin bool
	// An admin answered lately or took the conversation over.
	BotOff bool
	// Bot messages since midnight in Jakarta.
	RepliesToday int
}

func (s *Store) BotState(ctx context.Context, userID int64, midnight time.Time) (botState, error) {
	var b botState
	err := s.pool.QueryRow(ctx,
		`SELECT needs_admin, COALESCE(bot_off_until > now(), false),
		        (SELECT count(*) FROM messages WHERE user_id = $1 AND admin_id = 0 AND created_at >= $2)
		 FROM conversations WHERE user_id = $1`, userID, midnight).
		Scan(&b.NeedsAdmin, &b.BotOff, &b.RepliesToday)
	if errors.Is(err, pgx.ErrNoRows) {
		return b, errNotFound
	}
	return b, err
}

// SetBotWorking marks whether the bot is writing an answer, for "sedang mengetik".
func (s *Store) SetBotWorking(ctx context.Context, userID int64, working bool) error {
	_, err := s.pool.Exec(ctx, "UPDATE conversations SET bot_busy = $2 WHERE user_id = $1", userID, working)
	return err
}

// StopAllBotWork clears "sedang mengetik" left by a stop in the middle of an answer.
func (s *Store) StopAllBotWork(ctx context.Context) error {
	_, err := s.pool.Exec(ctx, "UPDATE conversations SET bot_busy = false WHERE bot_busy")
	return err
}

// Handoff marks that a person must answer, and reports whether the conversation only now
// started needing one, so the admins hear of it once. It makes the conversation when the
// customer has never written.
func (s *Store) Handoff(ctx context.Context, userID int64, reason string) (bool, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, "INSERT INTO conversations (user_id) VALUES ($1) ON CONFLICT DO NOTHING", userID); err != nil {
		return false, err
	}
	tag, err := tx.Exec(ctx,
		"UPDATE conversations SET needs_admin = true, needs_reason = $2 WHERE user_id = $1 AND NOT needs_admin",
		userID, reason)
	if err != nil {
		return false, err
	}
	if tag.RowsAffected() == 0 {
		return false, tx.Commit(ctx)
	}
	if _, err := tx.Exec(ctx, "INSERT INTO handoffs (user_id, reason) VALUES ($1, $2)", userID, reason); err != nil {
		return false, err
	}
	return true, tx.Commit(ctx)
}

// SetBotOff lets an admin take a conversation over, or give it back to the bot, which also
// settles what the customer needed.
func (s *Store) SetBotOff(ctx context.Context, userID int64, off bool) error {
	query := "UPDATE conversations SET bot_off_until = 'infinity' WHERE user_id = $1"
	if !off {
		query = "UPDATE conversations SET bot_off_until = NULL, needs_admin = false, needs_reason = '' WHERE user_id = $1"
	}
	tag, err := s.pool.Exec(ctx, query, userID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return errNotFound
	}
	return nil
}

// BotMonth counts the bot's messages and the handoffs since since.
func (s *Store) BotMonth(ctx context.Context, since time.Time) (replies, handoffs int, err error) {
	err = s.pool.QueryRow(ctx,
		`SELECT (SELECT count(*) FROM messages WHERE admin_id = 0 AND created_at >= $1),
		        (SELECT count(*) FROM handoffs WHERE created_at >= $1)`, since).Scan(&replies, &handoffs)
	return replies, handoffs, err
}
