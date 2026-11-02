package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

const dropCols = `id::text, title, total_qty, remaining, price_idr, max_per_buyer,
	hold_seconds, opens_at, closes_at, created_at`

func scanDrop(row pgx.Row) (Drop, error) {
	var d Drop
	err := row.Scan(&d.ID, &d.Title, &d.TotalQty, &d.Remaining, &d.PriceIDR,
		&d.MaxPerBuyer, &d.HoldSeconds, &d.OpensAt, &d.ClosesAt, &d.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Drop{}, ErrNotFound
	}
	return d, err
}

type CreateDropInput struct {
	Title       string
	TotalQty    int
	PriceIDR    int64
	MaxPerBuyer int
	HoldSeconds int
	OpensAt     time.Time
	ClosesAt    time.Time
}

func (s *Store) CreateDrop(ctx context.Context, in CreateDropInput) (Drop, error) {
	return scanDrop(s.pool.QueryRow(ctx, `
		INSERT INTO drops (title, total_qty, remaining, price_idr, max_per_buyer, hold_seconds, opens_at, closes_at)
		VALUES ($1, $2, $2, $3, $4, $5, $6, $7)
		RETURNING `+dropCols,
		in.Title, in.TotalQty, in.PriceIDR, in.MaxPerBuyer, in.HoldSeconds, in.OpensAt, in.ClosesAt))
}

func (s *Store) GetDrop(ctx context.Context, id string) (Drop, error) {
	return scanDrop(s.pool.QueryRow(ctx, `SELECT `+dropCols+` FROM drops WHERE id = $1`, id))
}

func (s *Store) ListDrops(ctx context.Context, limit int) ([]Drop, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+dropCols+` FROM drops ORDER BY created_at DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []Drop{}
	for rows.Next() {
		d, err := scanDrop(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}
