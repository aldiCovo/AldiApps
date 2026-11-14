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
	// No limit of the shop's own: only the most the quota column, an INTEGER, holds.
	maxVoucherQuota   = 2_147_483_647
	maxVoucherPerUser = 100
	maxVouchersListed = 200

	audienceHidden = "hidden"
	audienceAll    = "all"
	audienceUser   = "user"

	// Vouchers that ended this long ago still show, as ended, on the customer's page.
	endedVoucherShownFor = "30 days"
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
	// Shown in the shop's bar above the header while it can be used.
	Banner bool `json:"banner"`
	// Offered in the shop's sign-up popup while it can be used.
	Signup bool `json:"signup"`
	// Who sees it on their account page: hidden, all, or user (only UserID, who alone can use it).
	Audience  string    `json:"audience"`
	UserID    *int64    `json:"user_id"`
	Used      int64     `json:"used"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
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
	Banner       bool      `json:"banner"`
	Signup       bool      `json:"signup"`
	Audience     string    `json:"audience"`
	UserID       *int64    `json:"user_id"`
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
	switch in.Audience {
	case "":
		// Admin pages from before audiences send none.
		in.Audience = audienceHidden
		fallthrough
	case audienceHidden, audienceAll:
		if in.UserID != nil {
			return errors.New("user_id is only for a voucher with audience user")
		}
	case audienceUser:
		if in.UserID == nil || *in.UserID < 1 {
			return errors.New("a voucher for one customer needs their user_id")
		}
		if in.Banner {
			return errors.New("a voucher for one customer can't be shown in the banner")
		}
		if in.Signup {
			return errors.New("a voucher for one customer can't be offered in the sign-up popup")
		}
	default:
		return errors.New("audience must be hidden, all or user")
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

const voucherColumns = "id, code, description, kind, value, max_discount, min_subtotal, quota, per_user_limit, starts_at, ends_at, active, banner, created_at, updated_at, audience, user_id, signup"

func scanVoucher(row pgx.Row) (Voucher, error) {
	var v Voucher
	err := row.Scan(&v.ID, &v.Code, &v.Description, &v.Kind, &v.Value, &v.MaxDiscount, &v.MinSubtotal, &v.Quota,
		&v.PerUserLimit, &v.StartsAt, &v.EndsAt, &v.Active, &v.Banner, &v.CreatedAt, &v.UpdatedAt, &v.Audience, &v.UserID, &v.Signup)
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
	// A voucher for one customer is, for anyone else, a code that doesn't exist.
	query := `SELECT ` + voucherColumns + `, now() < starts_at, now() >= ends_at FROM vouchers
		WHERE code = $1 AND active AND (audience <> 'user' OR user_id = $2)`
	if lock {
		query += ` FOR UPDATE`
	}
	var v Voucher
	var notStarted, expired bool
	err := tx.QueryRow(ctx, query, normalizeCode(code), userID).Scan(&v.ID, &v.Code, &v.Description, &v.Kind, &v.Value, &v.MaxDiscount,
		&v.MinSubtotal, &v.Quota, &v.PerUserLimit, &v.StartsAt, &v.EndsAt, &v.Active, &v.Banner, &v.CreatedAt, &v.UpdatedAt,
		&v.Audience, &v.UserID, &v.Signup, &notStarted, &expired)
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
			&v.PerUserLimit, &v.StartsAt, &v.EndsAt, &v.Active, &v.Banner, &v.CreatedAt, &v.UpdatedAt, &v.Audience, &v.UserID, &v.Signup, &v.Used)
		return v, err
	})
}

func (s *Store) CreateVoucher(ctx context.Context, in voucherInput) (Voucher, error) {
	v, err := scanVoucher(s.pool.QueryRow(ctx,
		`INSERT INTO vouchers (code, description, kind, value, max_discount, min_subtotal, quota, per_user_limit, starts_at, ends_at, active, banner, audience, user_id, signup)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15) RETURNING `+voucherColumns,
		in.Code, in.Description, in.Kind, in.Value, in.MaxDiscount, in.MinSubtotal, in.Quota, in.PerUserLimit, in.StartsAt, in.EndsAt, in.Active, in.Banner, in.Audience, in.UserID, in.Signup))
	return v, voucherError(err)
}

// UpdateVoucher changes everything, the code too. Orders made with it keep the code they had.
func (s *Store) UpdateVoucher(ctx context.Context, id int64, in voucherInput) (Voucher, error) {
	v, err := scanVoucher(s.pool.QueryRow(ctx,
		`UPDATE vouchers SET code = $2, description = $3, kind = $4, value = $5, max_discount = $6, min_subtotal = $7,
			quota = $8, per_user_limit = $9, starts_at = $10, ends_at = $11, active = $12, banner = $13,
			audience = $14, user_id = $15, signup = $16, updated_at = now()
		 WHERE id = $1 RETURNING `+voucherColumns,
		id, in.Code, in.Description, in.Kind, in.Value, in.MaxDiscount, in.MinSubtotal, in.Quota, in.PerUserLimit, in.StartsAt, in.EndsAt, in.Active, in.Banner, in.Audience, in.UserID, in.Signup))
	if err != nil {
		return Voucher{}, voucherError(err)
	}
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM orders WHERE `+holdsVoucher, id).Scan(&v.Used); err != nil {
		return Voucher{}, err
	}
	return v, nil
}

// BannerVoucher is the voucher the shop shows above its header: one marked for the banner,
// turned on, running now and not used up, the most recently changed first. It answers nil
// when there is none.
func (s *Store) BannerVoucher(ctx context.Context) (*Voucher, error) {
	return s.featuredVoucher(ctx, "banner")
}

// SignupVoucher is the voucher the shop offers in its sign-up popup, chosen like BannerVoucher.
func (s *Store) SignupVoucher(ctx context.Context) (*Voucher, error) {
	return s.featuredVoucher(ctx, "signup")
}

// featuredVoucher picks by column, which is only ever "banner" or "signup".
func (s *Store) featuredVoucher(ctx context.Context, column string) (*Voucher, error) {
	v, err := scanVoucher(s.pool.QueryRow(ctx,
		`SELECT `+voucherColumns+` FROM vouchers
		 WHERE `+column+` AND audience <> 'user' AND active AND starts_at <= now() AND now() < ends_at
		   AND (quota IS NULL OR quota > (SELECT count(*) FROM orders
		        WHERE voucher_id = vouchers.id AND status NOT IN ('rejected', 'cancelled')))
		 ORDER BY updated_at DESC, id DESC LIMIT 1`))
	if errors.Is(err, errVoucherNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &v, nil
}

// The hours a browser rests from the sign-up popup after closing it, as admins may choose:
// from 2 hours to 365 days.
const minSignupRestHours, maxSignupRestHours = 2, 365 * 24

// SignupRestHours is how many hours the same browser waits before it sees the popup again.
func (s *Store) SignupRestHours(ctx context.Context) (int, error) {
	var hours int
	err := s.pool.QueryRow(ctx, `SELECT rest_hours FROM signup_popup WHERE id = 1`).Scan(&hours)
	return hours, err
}

func (s *Store) SaveSignupRestHours(ctx context.Context, hours int) error {
	_, err := s.pool.Exec(ctx, `UPDATE signup_popup SET rest_hours = $1, updated_at = now() WHERE id = 1`, hours)
	return err
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

// Where a voucher stands for one customer, on their account page.
const (
	myVoucherUsable = "usable"
	myVoucherUsed   = "used"
	myVoucherUsedUp = "used_up"
	myVoucherEnded  = "ended"
)

// MyVoucher is a voucher as its customer sees it: the terms, not how many others used it.
type MyVoucher struct {
	Code        string    `json:"code"`
	Description string    `json:"description"`
	Kind        string    `json:"kind"`
	Value       int64     `json:"value"`
	MaxDiscount *int64    `json:"max_discount"`
	MinSubtotal int64     `json:"min_subtotal"`
	EndsAt      time.Time `json:"ends_at"`
	// Only for this customer, rather than for everyone.
	ForYou   bool  `json:"for_you"`
	UsesLeft int32 `json:"uses_left"`
	// usable, used (by this customer, as often as allowed), used_up (by everyone) or ended.
	State string `json:"state"`
	// The newest order of this customer that holds it, or 0.
	LastOrderID int64 `json:"last_order_id"`
}

// MyVouchers lists the vouchers a customer may see: those for everyone and those for them,
// turned on and started, including ones that ended in the last endedVoucherShownFor. Usable
// ones come first, the soonest to end first.
func (s *Store) MyVouchers(ctx context.Context, userID int64) ([]MyVoucher, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT v.code, v.description, v.kind, v.value, v.max_discount, v.min_subtotal, v.ends_at,
			v.audience = 'user', v.per_user_limit, v.quota, now() >= v.ends_at,
			count(o.id), count(o.id) FILTER (WHERE o.user_id = $1),
			COALESCE(max(o.id) FILTER (WHERE o.user_id = $1), 0)
		 FROM vouchers v
		 LEFT JOIN orders o ON o.voucher_id = v.id AND o.status NOT IN ('rejected', 'cancelled')
		 WHERE v.active AND v.starts_at <= now() AND v.ends_at > now() - interval '`+endedVoucherShownFor+`'
		   AND (v.audience = 'all' OR (v.audience = 'user' AND v.user_id = $1))
		 GROUP BY v.id
		 ORDER BY v.ends_at, v.id
		 LIMIT $2`, userID, maxVouchersListed)
	if err != nil {
		return nil, err
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (MyVoucher, error) {
		var v MyVoucher
		var perUser int32
		var quota *int32
		var ended bool
		var used, mine int64
		err := row.Scan(&v.Code, &v.Description, &v.Kind, &v.Value, &v.MaxDiscount, &v.MinSubtotal, &v.EndsAt,
			&v.ForYou, &perUser, &quota, &ended, &used, &mine, &v.LastOrderID)
		v.UsesLeft = max(perUser-int32(mine), 0)
		switch {
		case ended:
			v.State = myVoucherEnded
		case v.UsesLeft == 0:
			v.State = myVoucherUsed
		case quota != nil && used >= int64(*quota):
			v.State = myVoucherUsedUp
		default:
			v.State = myVoucherUsable
		}
		return v, err
	})
	if err != nil {
		return nil, err
	}
	// Usable ones first, keeping the order by end date within each group.
	usable := make([]MyVoucher, 0, len(list))
	var others []MyVoucher
	for _, v := range list {
		if v.State == myVoucherUsable {
			usable = append(usable, v)
		} else {
			others = append(others, v)
		}
	}
	return append(usable, others...), nil
}
