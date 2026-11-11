package main

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

// Customers review each product of an order once it's completed. order-service keeps the
// reviews because it knows who bought what, so every review is from a buyer.
const (
	maxReviewBody     = 1000
	defaultReviewPage = 10
	maxReviewPage     = 50
)

var (
	errBadRating     = errors.New("rating must be between 1 and 5")
	errLongReview    = errors.New("the review can be at most 1000 characters")
	errNotReviewable = errors.New("only completed orders can be reviewed")
	errNotInOrder    = errors.New("this product isn't in the order")

	// Statuses whose items count as sold.
	soldStatuses = []string{statusPaid, statusPacked, statusShipped, statusArrived, statusCompleted}
)

// MyReview is the customer's own review, shown on their order.
type MyReview struct {
	Rating    int16     `json:"rating"`
	Body      string    `json:"body"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Review is what everyone sees on the product page.
type Review struct {
	Rating    int16     `json:"rating"`
	Body      string    `json:"body"`
	Author    string    `json:"author"`
	Variant   string    `json:"variant"`
	CreatedAt time.Time `json:"created_at"`
}

type ReviewPage struct {
	Average float64 `json:"average"`
	Count   int64   `json:"count"`
	// Counts[0] is one star, Counts[4] five.
	Counts  [5]int64 `json:"counts"`
	Reviews []Review `json:"reviews"`
	// How many reviews match the filter, for paging.
	Matching int64 `json:"matching"`
}

type Rating struct {
	Average float64 `json:"average"`
	Count   int64   `json:"count"`
	Sold    int64   `json:"sold"`
}

// maskName shows the first and last letter of the first name only: Sari becomes S***i.
func maskName(name string) string {
	first, _, _ := strings.Cut(strings.TrimSpace(name), " ")
	letters := []rune(first)
	switch {
	case len(letters) == 0:
		return "Pembeli"
	case len(letters) <= 2:
		return string(unicode.ToUpper(letters[0])) + "***"
	}
	return string(unicode.ToUpper(letters[0])) + "***" + string(unicode.ToLower(letters[len(letters)-1]))
}

func cleanReviewBody(body string) (string, error) {
	body = strings.TrimSpace(strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' || !unicode.IsControl(r) {
			return r
		}
		return -1
	}, body))
	if utf8.RuneCountInString(body) > maxReviewBody {
		return "", errLongReview
	}
	return body, nil
}

// SaveReview adds or replaces the customer's review of one product in their completed order.
func (s *Store) SaveReview(ctx context.Context, userID, orderID, productID int64, rating int16, body string) (MyReview, error) {
	if rating < 1 || rating > 5 {
		return MyReview{}, errBadRating
	}
	body, err := cleanReviewBody(body)
	if err != nil {
		return MyReview{}, err
	}
	var review MyReview
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var status, recipient string
		err := tx.QueryRow(ctx,
			`SELECT status, COALESCE(shipping_address->>'recipient', '') FROM orders WHERE id = $1 AND user_id = $2`,
			orderID, userID).Scan(&status, &recipient)
		if errors.Is(err, pgx.ErrNoRows) {
			return errNotFound
		}
		if err != nil {
			return err
		}
		if status != statusCompleted {
			return errNotReviewable
		}
		var variant string
		err = tx.QueryRow(ctx,
			`SELECT string_agg(DISTINCT size || ' · ' || color, ', ') FROM order_items
			 WHERE order_id = $1 AND product_id = $2 HAVING count(*) > 0`, orderID, productID).Scan(&variant)
		if errors.Is(err, pgx.ErrNoRows) {
			return errNotInOrder
		}
		if err != nil {
			return err
		}
		return tx.QueryRow(ctx,
			`INSERT INTO reviews (order_id, product_id, user_id, rating, body, author, variant)
			 VALUES ($1, $2, $3, $4, $5, $6, $7)
			 ON CONFLICT (order_id, product_id) DO UPDATE SET rating = $4, body = $5, updated_at = now()
			 RETURNING rating, body, updated_at`,
			orderID, productID, userID, rating, body, maskName(recipient), variant).Scan(&review.Rating, &review.Body, &review.UpdatedAt)
	})
	return review, err
}

// attachMyReviews adds the customer's reviews to their completed orders.
func attachMyReviews(ctx context.Context, q querier, orders []Order) error {
	var ids []int64
	byID := make(map[int64]*Order)
	for i := range orders {
		if orders[i].Status == statusCompleted {
			ids = append(ids, orders[i].ID)
			byID[orders[i].ID] = &orders[i]
		}
	}
	if len(ids) == 0 {
		return nil
	}
	rows, err := q.Query(ctx, `SELECT order_id, product_id, rating, body, updated_at FROM reviews WHERE order_id = ANY($1)`, ids)
	if err != nil {
		return err
	}
	var orderID, productID int64
	var review MyReview
	_, err = pgx.ForEachRow(rows, []any{&orderID, &productID, &review.Rating, &review.Body, &review.UpdatedAt}, func() error {
		order := byID[orderID]
		if order.Reviews == nil {
			order.Reviews = map[int64]MyReview{}
		}
		order.Reviews[productID] = review
		return nil
	})
	return err
}

// ProductReviews is a product's rating summary and a page of its reviews, newest first,
// optionally only those with one number of stars.
func (s *Store) ProductReviews(ctx context.Context, productID int64, stars, limit, offset int) (ReviewPage, error) {
	page := ReviewPage{Reviews: []Review{}}
	rows, err := s.pool.Query(ctx, `SELECT rating, count(*) FROM reviews WHERE product_id = $1 GROUP BY rating`, productID)
	if err != nil {
		return page, err
	}
	var rating int16
	var count, sum int64
	if _, err := pgx.ForEachRow(rows, []any{&rating, &count}, func() error {
		page.Counts[rating-1] = count
		page.Count += count
		sum += int64(rating) * count
		return nil
	}); err != nil {
		return page, err
	}
	if page.Count == 0 {
		return page, nil
	}
	page.Average = roundRating(float64(sum) / float64(page.Count))
	page.Matching = page.Count
	if stars != 0 {
		page.Matching = page.Counts[stars-1]
	}
	rows, err = s.pool.Query(ctx,
		`SELECT rating, body, author, variant, created_at FROM reviews
		 WHERE product_id = $1 AND ($2 = 0 OR rating = $2)
		 ORDER BY created_at DESC, order_id DESC LIMIT $3 OFFSET $4`, productID, stars, limit, offset)
	if err != nil {
		return page, err
	}
	page.Reviews, err = pgx.CollectRows(rows, pgx.RowToStructByPos[Review])
	return page, err
}

// Ratings are every product's average stars and how many have been sold, for the cards. Only
// products with a review or a sale are listed.
func (s *Store) Ratings(ctx context.Context) (map[int64]Rating, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT COALESCE(r.product_id, sold.product_id), COALESCE(r.total, 0), COALESCE(r.count, 0), COALESCE(sold.quantity, 0)
		 FROM (SELECT product_id, sum(rating) AS total, count(*) AS count FROM reviews GROUP BY product_id) r
		 FULL JOIN (
		     SELECT i.product_id, sum(i.quantity) AS quantity FROM order_items i JOIN orders o ON o.id = i.order_id
		     WHERE o.status = ANY($1) GROUP BY i.product_id
		 ) sold ON sold.product_id = r.product_id`, soldStatuses)
	if err != nil {
		return nil, err
	}
	ratings := map[int64]Rating{}
	var productID, total, count, sold int64
	_, err = pgx.ForEachRow(rows, []any{&productID, &total, &count, &sold}, func() error {
		rating := Rating{Count: count, Sold: sold}
		if count > 0 {
			rating.Average = roundRating(float64(total) / float64(count))
		}
		ratings[productID] = rating
		return nil
	})
	return ratings, err
}

func roundRating(average float64) float64 {
	return float64(int64(average*10+0.5)) / 10
}
