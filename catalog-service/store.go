package main

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// maxStock keeps stock + delta inside a Postgres INTEGER when delta is at most maxStock too.
const maxStock = 1_000_000_000

const (
	pgForeignKeyViolation = "23503"
	pgUniqueViolation     = "23505"
)

var (
	errNotFound         = errors.New("product not found")
	errCategoryNotFound = errors.New("category not found")
	errVariantNotFound  = errors.New("variant not found")
	errUnknownCategory  = errors.New("category_id does not exist")
	errCategoryExists   = errors.New("a category with this name already exists")
	errCategoryInUse    = errors.New("category still has products")
	errVariantExists    = errors.New("this product already has a variant with this size and color")
	errStockOutOfRange  = errors.New("stock would leave the allowed range")
)

type Category struct {
	ID        int64     `json:"id"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type Variant struct {
	ID        int64     `json:"id"`
	ProductID int64     `json:"product_id"`
	Size      string    `json:"size"`
	Color     string    `json:"color"`
	Stock     int32     `json:"stock"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type Product struct {
	ID          int64     `json:"id"`
	CategoryID  *int64    `json:"category_id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	Price       int64     `json:"price"`
	Variants    []Variant `json:"variants"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

const (
	categoryColumns = "id, name, created_at, updated_at"
	productColumns  = "id, category_id, name, description, price, created_at, updated_at"
	variantColumns  = "id, product_id, size, color, stock, created_at, updated_at"
)

type Store struct {
	pool *pgxpool.Pool
}

func hasPgCode(err error, code string) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == code
}

func scanCategory(row pgx.Row) (Category, error) {
	var c Category
	err := row.Scan(&c.ID, &c.Name, &c.CreatedAt, &c.UpdatedAt)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return Category{}, errCategoryNotFound
	case hasPgCode(err, pgUniqueViolation):
		return Category{}, errCategoryExists
	}
	return c, err
}

func (s *Store) CreateCategory(ctx context.Context, name string) (Category, error) {
	return scanCategory(s.pool.QueryRow(ctx,
		`INSERT INTO categories (name) VALUES ($1) RETURNING `+categoryColumns, name))
}

func (s *Store) GetCategory(ctx context.Context, id int64) (Category, error) {
	return scanCategory(s.pool.QueryRow(ctx,
		`SELECT `+categoryColumns+` FROM categories WHERE id = $1`, id))
}

func (s *Store) ListCategories(ctx context.Context) ([]Category, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+categoryColumns+` FROM categories ORDER BY lower(name)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	categories := []Category{}
	for rows.Next() {
		c, err := scanCategory(rows)
		if err != nil {
			return nil, err
		}
		categories = append(categories, c)
	}
	return categories, rows.Err()
}

func (s *Store) UpdateCategory(ctx context.Context, id int64, name string) (Category, error) {
	return scanCategory(s.pool.QueryRow(ctx,
		`UPDATE categories SET name = $2, updated_at = now() WHERE id = $1 RETURNING `+categoryColumns,
		id, name))
}

func (s *Store) DeleteCategory(ctx context.Context, id int64) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM categories WHERE id = $1`, id)
	if hasPgCode(err, pgForeignKeyViolation) {
		return errCategoryInUse
	}
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return errCategoryNotFound
	}
	return nil
}

func scanProduct(row pgx.Row) (Product, error) {
	p := Product{Variants: []Variant{}}
	err := row.Scan(&p.ID, &p.CategoryID, &p.Name, &p.Description, &p.Price, &p.CreatedAt, &p.UpdatedAt)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return Product{}, errNotFound
	case hasPgCode(err, pgForeignKeyViolation):
		return Product{}, errUnknownCategory
	}
	return p, err
}

// attachVariants loads the variants of all products in one query.
func (s *Store) attachVariants(ctx context.Context, products []Product) error {
	ids := make([]int64, len(products))
	byID := make(map[int64]*Product, len(products))
	for i := range products {
		ids[i] = products[i].ID
		byID[products[i].ID] = &products[i]
	}
	rows, err := s.pool.Query(ctx,
		`SELECT `+variantColumns+` FROM product_variants WHERE product_id = ANY($1) ORDER BY id`, ids)
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		v, err := scanVariant(rows)
		if err != nil {
			return err
		}
		p := byID[v.ProductID]
		p.Variants = append(p.Variants, v)
	}
	return rows.Err()
}

func (s *Store) withVariants(ctx context.Context, p Product, err error) (Product, error) {
	if err != nil {
		return Product{}, err
	}
	products := []Product{p}
	if err := s.attachVariants(ctx, products); err != nil {
		return Product{}, err
	}
	return products[0], nil
}

func (s *Store) Create(ctx context.Context, categoryID *int64, name, description string, price int64) (Product, error) {
	return scanProduct(s.pool.QueryRow(ctx,
		`INSERT INTO products (category_id, name, description, price)
		 VALUES ($1, $2, $3, $4)
		 RETURNING `+productColumns,
		categoryID, name, description, price))
}

func (s *Store) Get(ctx context.Context, id int64) (Product, error) {
	p, err := scanProduct(s.pool.QueryRow(ctx,
		`SELECT `+productColumns+` FROM products WHERE id = $1`, id))
	return s.withVariants(ctx, p, err)
}

// List returns all products, or only one category's when categoryID is set.
func (s *Store) List(ctx context.Context, categoryID *int64, limit, offset int) ([]Product, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+productColumns+` FROM products
		 WHERE $1::bigint IS NULL OR category_id = $1
		 ORDER BY id LIMIT $2 OFFSET $3`,
		categoryID, limit, offset)
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
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := s.attachVariants(ctx, products); err != nil {
		return nil, err
	}
	return products, nil
}

func (s *Store) Update(ctx context.Context, id int64, categoryID *int64, name, description string, price int64) (Product, error) {
	p, err := scanProduct(s.pool.QueryRow(ctx,
		`UPDATE products SET category_id = $2, name = $3, description = $4, price = $5, updated_at = now()
		 WHERE id = $1
		 RETURNING `+productColumns,
		id, categoryID, name, description, price))
	return s.withVariants(ctx, p, err)
}

// Delete also removes the product's variants, through ON DELETE CASCADE.
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

func scanVariant(row pgx.Row) (Variant, error) {
	var v Variant
	err := row.Scan(&v.ID, &v.ProductID, &v.Size, &v.Color, &v.Stock, &v.CreatedAt, &v.UpdatedAt)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return Variant{}, errVariantNotFound
	case hasPgCode(err, pgUniqueViolation):
		return Variant{}, errVariantExists
	case hasPgCode(err, pgForeignKeyViolation):
		return Variant{}, errNotFound
	}
	return v, err
}

func (s *Store) CreateVariant(ctx context.Context, productID int64, size, color string, stock int32) (Variant, error) {
	return scanVariant(s.pool.QueryRow(ctx,
		`INSERT INTO product_variants (product_id, size, color, stock)
		 VALUES ($1, $2, $3, $4)
		 RETURNING `+variantColumns,
		productID, size, color, stock))
}

func (s *Store) GetVariant(ctx context.Context, id int64) (Variant, error) {
	return scanVariant(s.pool.QueryRow(ctx,
		`SELECT `+variantColumns+` FROM product_variants WHERE id = $1`, id))
}

// UpdateVariant leaves stock alone, so a stale edit can't overwrite a concurrent stock change.
func (s *Store) UpdateVariant(ctx context.Context, id int64, size, color string) (Variant, error) {
	return scanVariant(s.pool.QueryRow(ctx,
		`UPDATE product_variants SET size = $2, color = $3, updated_at = now()
		 WHERE id = $1
		 RETURNING `+variantColumns,
		id, size, color))
}

func (s *Store) DeleteVariant(ctx context.Context, id int64) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM product_variants WHERE id = $1`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return errVariantNotFound
	}
	return nil
}

// AdjustStock applies delta in one statement, so concurrent orders can't oversell.
func (s *Store) AdjustStock(ctx context.Context, id int64, delta int32) (Variant, error) {
	v, err := scanVariant(s.pool.QueryRow(ctx,
		`UPDATE product_variants SET stock = stock + $2, updated_at = now()
		 WHERE id = $1 AND stock + $2 BETWEEN 0 AND $3
		 RETURNING `+variantColumns,
		id, delta, maxStock))
	if !errors.Is(err, errVariantNotFound) {
		return v, err
	}
	// No row changed: either the variant is missing or the new stock is out of range.
	if _, err := s.GetVariant(ctx, id); err != nil {
		return Variant{}, err
	}
	return Variant{}, errStockOutOfRange
}
