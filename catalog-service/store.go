package main

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// maxStock keeps stock + delta inside a Postgres INTEGER when delta is at most maxStock too.
const maxStock = 1_000_000_000

var (
	errNotFound        = errors.New("product not found")
	errStockOutOfRange = errors.New("stock would leave the allowed range")
)

type Product struct {
	ID          int64     `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	Price       int64     `json:"price"`
	Stock       int32     `json:"stock"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

const productColumns = "id, name, description, price, stock, created_at, updated_at"

type Store struct {
	pool *pgxpool.Pool
}

func scanProduct(row pgx.Row) (Product, error) {
	var p Product
	err := row.Scan(&p.ID, &p.Name, &p.Description, &p.Price, &p.Stock, &p.CreatedAt, &p.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Product{}, errNotFound
	}
	return p, err
}

func (s *Store) Create(ctx context.Context, name, description string, price int64, stock int32) (Product, error) {
	return scanProduct(s.pool.QueryRow(ctx,
		`INSERT INTO products (name, description, price, stock)
		 VALUES ($1, $2, $3, $4)
		 RETURNING `+productColumns,
		name, description, price, stock))
}

func (s *Store) Get(ctx context.Context, id int64) (Product, error) {
	return scanProduct(s.pool.QueryRow(ctx,
		`SELECT `+productColumns+` FROM products WHERE id = $1`, id))
}

func (s *Store) List(ctx context.Context, limit, offset int) ([]Product, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+productColumns+` FROM products ORDER BY id LIMIT $1 OFFSET $2`, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	products := []Product{}
	for rows.Next() {
		p, err := scanProduct(rows)
		if err != nil {
			return nil, err
		}
		products = append(products, p)
	}
	return products, rows.Err()
}

// Update leaves stock alone, so a stale edit can't overwrite a concurrent stock change.
func (s *Store) Update(ctx context.Context, id int64, name, description string, price int64) (Product, error) {
	return scanProduct(s.pool.QueryRow(ctx,
		`UPDATE products SET name = $2, description = $3, price = $4, updated_at = now()
		 WHERE id = $1
		 RETURNING `+productColumns,
		id, name, description, price))
}

func (s *Store) Delete(ctx context.Context, id int64) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM products WHERE id = $1`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return errNotFound
	}
	return nil
}

// AdjustStock applies delta in one statement, so concurrent orders can't oversell.
func (s *Store) AdjustStock(ctx context.Context, id int64, delta int32) (Product, error) {
	p, err := scanProduct(s.pool.QueryRow(ctx,
		`UPDATE products SET stock = stock + $2, updated_at = now()
		 WHERE id = $1 AND stock + $2 BETWEEN 0 AND $3
		 RETURNING `+productColumns,
		id, delta, maxStock))
	if !errors.Is(err, errNotFound) {
		return p, err
	}
	// No row changed: either the product is missing or the new stock is out of range.
	if _, err := s.Get(ctx, id); err != nil {
		return Product{}, err
	}
	return Product{}, errStockOutOfRange
}
