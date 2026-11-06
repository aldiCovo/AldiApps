package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

const (
	voucherPercent      = "percent"
	voucherAmount       = "amount"
	voucherFreeShipping = "free_shipping"

	maxVoucherDescription = 200
	maxVoucherRupiah      = 100_000_000
	maxVoucherQuota       = 1_000_000
	maxVoucherPerUser     = 100
	maxVouchersListed     = 200
)

var (
	voucherCodePattern = regexp.MustCompile(`^[A-Z0-9]{3,20}$`)

	errVoucherNotFound  = errors.New("voucher not found")
	errVoucherCodeTaken = errors.New("another voucher has this code")
	errVoucherInUse     = errors.New("orders use this voucher, so it can only be turned off")
)

// Voucher is a code that takes money off an order. Used counts the orders holding it now:
// orders that failed or were cancelled give their use back.
type Voucher struct {
	ID           int64     `json:"id"`
	Code         string    `json:"code"`
	Description  string    `json:"description"`
	Kind         string    `json:"kind"`
	Value        int64     `json:"value"`
	MaxDiscount  *int64    `json:"max_discount"`
	MinSubtotal  int64     `json:"min_subtotal"`
	Quota        *int32    `json:"quota"`
	PerUserLimit int32     `json:"per_user_limit"`
	StartsAt     time.Time `json:"starts_at"`
	EndsAt       time.Time `json:"ends_at"`
	Active       bool      `json:"active"`
	Used         int64     `json:"used"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// voucherInput is what the admin pages send to make or change a voucher.
type voucherInput struct {
	Code         string    `json:"code"`
	Description  string    `json:"description"`
	Kind         string    `json:"kind"`
	Value        int64     `json:"value"`
	MaxDiscount  *int64    `json:"max_discount"`
	MinSubtotal  int64     `json:"min_subtotal"`
	Quota        *int32    `json:"quota"`
	PerUserLimit int32     `json:"per_user_limit"`
	StartsAt     time.Time `json:"starts_at"`
	EndsAt       time.Time `json:"ends_at"`
	Active       bool      `json:"active"`
}

func normalizeCode(code string) string {
	return strings.ToUpper(strings.TrimSpace(code))
}

func (in *voucherInput) validate() error {
	in.Code = normalizeCode(in.Code)
	in.Description = strings.TrimSpace(in.Description)
	switch {
	case !voucherCodePattern.MatchString(in.Code):
		return errors.New("code must be 3 to 20 letters or digits")
	case utf8.RuneCountInString(in.Description) > maxVoucherDescription:
		return fmt.Errorf("description must be at most %d characters", maxVoucherDescription)
	case in.MinSubtotal < 0 || in.MinSubtotal > maxVoucherRupiah:
		return fmt.Errorf("min_subtotal must be between 0 and %d", maxVoucherRupiah)
	case in.Quota != nil && (*in.Quota < 1 || *in.Quota > maxVoucherQuota):
		return fmt.Errorf("quota must be empty or between 1 and %d", maxVoucherQuota)
	case in.PerUserLimit < 1 || in.PerUserLimit > maxVoucherPerUser:
		return fmt.Errorf("per_user_limit must be between 1 and %d", maxVoucherPerUser)
	case in.StartsAt.IsZero() || in.EndsAt.IsZero() || !in.EndsAt.After(in.StartsAt):
		return errors.New("ends_at must be after starts_at")
	case in.MaxDiscount != nil && (*in.MaxDiscount < 1 || *in.MaxDiscount > maxVoucherRupiah):
		return fmt.Errorf("max_discount must be empty or between 1 and %d", maxVoucherRupiah)
	}
	switch in.Kind {
	case voucherPercent:
		if in.Value < 1 || in.Value > 100 {
			return errors.New("a percent voucher's value must be between 1 and 100")
		}
	case voucherAmount:
		if in.Value < 1 || in.Value > maxVoucherRupiah {
			return fmt.Errorf("an amount voucher's value must be between 1 and %d", maxVoucherRupiah)
		}
		if in.MaxDiscount != nil {
			return errors.New("an amount voucher takes no max_discount")
		}
	case voucherFreeShipping:
		if in.Value != 0 {
			return errors.New("a free_shipping voucher's value must be 0")
		}
	default:
		return errors.New("kind must be percent, amount or free_shipping")
	}
	return nil
}

// discount is what the voucher takes off. It never takes more than the items cost, or for
// free shipping, more than the shipping.
func (v *Voucher) discount(subtotal, shippingCost int64) int64 {
	var d int64
	switch v.Kind {
	case voucherPercent:
		d = subtotal * v.Value / 100
	case voucherAmount:
		d = v.Value
	case voucherFreeShipping:
		d = shippingCost
	}
	if v.MaxDiscount != nil {
		d = min(d, *v.MaxDiscount)
	}
	if v.Kind == voucherFreeShipping {
		return min(d, shippingCost)
	}
	return min(d, subtotal)
}

// Reasons a customer's voucher can't be used, which the shop shows in its own words.
const (
	refusedNotFound    = "not_found"
	refusedNotStarted  = "not_started"
	refusedExpired     = "expired"
	refusedUsedUp      = "used_up"
	refusedAlreadyUsed = "already_used"
	refusedMinSubtotal = "min_subtotal"
)

type voucherRefusal struct {
	Reason      string
	MinSubtotal int64
}

func (e *voucherRefusal) Error() string { return "the voucher can't be used: " + e.Reason }

const voucherColumns = "id, code, description, kind, value, max_discount, min_subtotal, quota, per_user_limit, starts_at, ends_at, active, created_at, updated_at"

func scanVoucher(row pgx.Row) (Voucher, error) {
	var v Voucher
	err := row.Scan(&v.ID, &v.Code, &v.Description, &v.Kind, &v.Value, &v.MaxDiscount, &v.MinSubtotal, &v.Quota,
		&v.PerUserLimit, &v.StartsAt, &v.EndsAt, &v.Active, &v.CreatedAt, &v.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Voucher{}, errVoucherNotFound
	}
	return v, err
}

// Orders that still hold their voucher. One that failed or was cancelled gives it back.
const holdsVoucher = "voucher_id = $1 AND status NOT IN ('rejected', 'cancelled')"

// applyVoucher checks a code for a user's order and returns the voucher and its discount.
// With lock, the voucher's row stays locked until tx ends, so orders made at the same time
// can't together go past its quota.
func applyVoucher(ctx context.Context, tx pgx.Tx, lock bool, userID int64, code string, subtotal, shippingCost int64) (Voucher, int64, error) {
	query := `SELECT ` + voucherColumns + `, now() < starts_at, now() >= ends_at FROM vouchers WHERE code = $1 AND active`
	if lock {
		query += ` FOR UPDATE`
	}
	var v Voucher
	var notStarted, expired bool
	err := tx.QueryRow(ctx, query, normalizeCode(code)).Scan(&v.ID, &v.Code, &v.Description, &v.Kind, &v.Value, &v.MaxDiscount,
		&v.MinSubtotal, &v.Quota, &v.PerUserLimit, &v.StartsAt, &v.EndsAt, &v.Active, &v.CreatedAt, &v.UpdatedAt, &notStarted, &expired)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return Voucher{}, 0, &voucherRefusal{Reason: refusedNotFound}
	case err != nil:
		return Voucher{}, 0, err
	case notStarted:
		return Voucher{}, 0, &voucherRefusal{Reason: refusedNotStarted}
	case expired:
		return Voucher{}, 0, &voucherRefusal{Reason: refusedExpired}
	case subtotal < v.MinSubtotal:
		return Voucher{}, 0, &voucherRefusal{Reason: refusedMinSubtotal, MinSubtotal: v.MinSubtotal}
	}
	var used, mine int64
	if err := tx.QueryRow(ctx,
		`SELECT count(*), count(*) FILTER (WHERE user_id = $2) FROM orders WHERE `+holdsVoucher,
		v.ID, userID).Scan(&used, &mine); err != nil {
		return Voucher{}, 0, err
	}
	if mine >= int64(v.PerUserLimit) {
		return Voucher{}, 0, &voucherRefusal{Reason: refusedAlreadyUsed}
	}
	if v.Quota != nil && used >= int64(*v.Quota) {
		return Voucher{}, 0, &voucherRefusal{Reason: refusedUsedUp}
	}
	v.Used = used
	return v, v.discount(subtotal, shippingCost), nil
}

// CheckVoucher tells the checkout what a code would take off, without holding it. The order
// checks it again when it's made.
func (s *Store) CheckVoucher(ctx context.Context, userID int64, code string, subtotal, shippingCost int64) (v Voucher, discount int64, err error) {
	err = pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		v, discount, err = applyVoucher(ctx, tx, false, userID, code, subtotal, shippingCost)
		return err
	})
	return v, discount, err
}

func (s *Store) ListVouchers(ctx context.Context) ([]Voucher, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+voucherColumns+`,
			(SELECT count(*) FROM orders WHERE voucher_id = vouchers.id AND status NOT IN ('rejected', 'cancelled'))
		 FROM vouchers ORDER BY id DESC LIMIT $1`, maxVouchersListed)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (Voucher, error) {
		var v Voucher
		err := row.Scan(&v.ID, &v.Code, &v.Description, &v.Kind, &v.Value, &v.MaxDiscount, &v.MinSubtotal, &v.Quota,
			&v.PerUserLimit, &v.StartsAt, &v.EndsAt, &v.Active, &v.CreatedAt, &v.UpdatedAt, &v.Used)
		return v, err
	})
}

func (s *Store) CreateVoucher(ctx context.Context, in voucherInput) (Voucher, error) {
	v, err := scanVoucher(s.pool.QueryRow(ctx,
		`INSERT INTO vouchers (code, description, kind, value, max_discount, min_subtotal, quota, per_user_limit, starts_at, ends_at, active)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11) RETURNING `+voucherColumns,
		in.Code, in.Description, in.Kind, in.Value, in.MaxDiscount, in.MinSubtotal, in.Quota, in.PerUserLimit, in.StartsAt, in.EndsAt, in.Active))
	return v, voucherError(err)
}

// UpdateVoucher changes everything, the code too. Orders made with it keep the code they had.
func (s *Store) UpdateVoucher(ctx context.Context, id int64, in voucherInput) (Voucher, error) {
	v, err := scanVoucher(s.pool.QueryRow(ctx,
		`UPDATE vouchers SET code = $2, description = $3, kind = $4, value = $5, max_discount = $6, min_subtotal = $7,
			quota = $8, per_user_limit = $9, starts_at = $10, ends_at = $11, active = $12, updated_at = now()
		 WHERE id = $1 RETURNING `+voucherColumns,
		id, in.Code, in.Description, in.Kind, in.Value, in.MaxDiscount, in.MinSubtotal, in.Quota, in.PerUserLimit, in.StartsAt, in.EndsAt, in.Active))
	if err != nil {
		return Voucher{}, voucherError(err)
	}
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM orders WHERE `+holdsVoucher, id).Scan(&v.Used); err != nil {
		return Voucher{}, err
	}
	return v, nil
}

// DeleteVoucher removes a voucher no order has used. One that was used can only be turned off.
func (s *Store) DeleteVoucher(ctx context.Context, id int64) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM vouchers WHERE id = $1`, id)
	if err != nil {
		return voucherError(err)
	}
	if tag.RowsAffected() == 0 {
		return errVoucherNotFound
	}
	return nil
}

func voucherError(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "23505":
			return errVoucherCodeTaken
		case "23503":
			return errVoucherInUse
		}
	}
	return err
}

// writeVoucherRefusal answers 422 with the reason, so the shop can explain it.
func writeVoucherRefusal(w http.ResponseWriter, refusal *voucherRefusal) {
	body := map[string]any{"error": refusal.Error(), "reason": refusal.Reason}
	if refusal.Reason == refusedMinSubtotal {
		body["min_subtotal"] = refusal.MinSubtotal
	}
	writeJSON(w, http.StatusUnprocessableEntity, body)
}
