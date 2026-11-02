// Package store holds every SQL statement. Handlers never touch the pool.
package store

import (
	"errors"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrNotFound            = errors.New("not found")
	ErrSoldOut             = errors.New("not enough remaining")
	ErrAlreadyClaimed      = errors.New("buyer already has an active claim")
	ErrNotOpen             = errors.New("drop is not open")
	ErrQtyExceedsLimit     = errors.New("qty exceeds max per buyer")
	ErrIdempotencyMismatch = errors.New("idempotency key reused with different request")
)

type Store struct {
	pool *pgxpool.Pool
}

func New(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

type Drop struct {
	ID          string    `json:"id"`
	Title       string    `json:"title"`
	TotalQty    int       `json:"total_qty"`
	Remaining   int       `json:"remaining"`
	PriceIDR    int64     `json:"price_idr"`
	MaxPerBuyer int       `json:"max_per_buyer"`
	HoldSeconds int       `json:"hold_seconds"`
	OpensAt     time.Time `json:"opens_at"`
	ClosesAt    time.Time `json:"closes_at"`
	CreatedAt   time.Time `json:"created_at"`
}

type Claim struct {
	ID             string     `json:"id"`
	DropID         string     `json:"drop_id"`
	BuyerRef       string     `json:"buyer_ref"`
	Qty            int        `json:"qty"`
	Status         string     `json:"status"`
	ExpiresAt      time.Time  `json:"expires_at"`
	IdempotencyKey string     `json:"idempotency_key"`
	CreatedAt      time.Time  `json:"created_at"`
	ConfirmedAt    *time.Time `json:"confirmed_at,omitempty"`
}
