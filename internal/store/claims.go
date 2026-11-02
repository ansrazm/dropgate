package store

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

const claimCols = `id::text, drop_id::text, buyer_ref, qty, status, expires_at,
	idempotency_key, created_at, confirmed_at`

func scanClaim(row pgx.Row) (Claim, error) {
	var c Claim
	err := row.Scan(&c.ID, &c.DropID, &c.BuyerRef, &c.Qty, &c.Status, &c.ExpiresAt,
		&c.IdempotencyKey, &c.CreatedAt, &c.ConfirmedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Claim{}, ErrNotFound
	}
	return c, err
}

type ClaimInput struct {
	DropID         string
	BuyerRef       string
	Qty            int
	IdempotencyKey string
}

// errGuardRejected means the guarded UPDATE matched no row. It is internal:
// CreateClaim turns it into a specific, caller-facing error.
var errGuardRejected = errors.New("guard rejected")

// CreateClaim holds `Qty` units of a drop for the buyer. The bool result is
// true when the call was an idempotent replay of an earlier request.
func (s *Store) CreateClaim(ctx context.Context, in ClaimInput) (Claim, bool, error) {
	// Fast path: this exact request already succeeded before.
	if existing, err := s.claimByKey(ctx, in.DropID, in.IdempotencyKey); err == nil {
		return replay(existing, in)
	} else if !errors.Is(err, ErrNotFound) {
		return Claim{}, false, err
	}

	c, err := s.tryClaim(ctx, in)
	switch {
	case err == nil:
		return c, false, nil
	case errors.Is(err, errGuardRejected):
		return Claim{}, false, s.explainRejection(ctx, in)
	case isUniqueViolation(err):
		// Two ways to get here, and the transaction has been rolled back, so
		// no stock was taken either way:
		//  1. a concurrent request with the same key won the race (replay)
		//  2. the buyer already holds an active claim on this drop
		if existing, e := s.claimByKey(ctx, in.DropID, in.IdempotencyKey); e == nil {
			return replay(existing, in)
		}
		return Claim{}, false, ErrAlreadyClaimed
	default:
		return Claim{}, false, err
	}
}

// tryClaim is the core: take stock and record the claim in ONE transaction.
// If the INSERT fails (duplicate key, duplicate buyer), the stock decrement
// rolls back with it, so remaining can never drift from the claims table.
func (s *Store) tryClaim(ctx context.Context, in ClaimInput) (Claim, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Claim{}, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after Commit

	// The guard lives in the WHERE clause: Postgres re-checks it under the row
	// lock, so concurrent claims are serialized per drop and can't oversell.
	var holdSeconds int
	err = tx.QueryRow(ctx, `
		UPDATE drops
		   SET remaining = remaining - $2
		 WHERE id = $1
		   AND remaining >= $2
		   AND $2 <= max_per_buyer
		   AND now() >= opens_at
		   AND now() <  closes_at
		RETURNING hold_seconds`, in.DropID, in.Qty).Scan(&holdSeconds)
	if errors.Is(err, pgx.ErrNoRows) {
		return Claim{}, errGuardRejected
	}
	if err != nil {
		return Claim{}, err
	}

	c, err := scanClaim(tx.QueryRow(ctx, `
		INSERT INTO claims (drop_id, buyer_ref, qty, status, expires_at, idempotency_key)
		VALUES ($1, $2, $3, 'HELD', now() + $4::int * interval '1 second', $5)
		RETURNING `+claimCols,
		in.DropID, in.BuyerRef, in.Qty, holdSeconds, in.IdempotencyKey))
	if err != nil {
		return Claim{}, err
	}

	if err := tx.Commit(ctx); err != nil {
		return Claim{}, err
	}
	return c, nil
}

// explainRejection runs after the guard said no, to tell the caller why.
func (s *Store) explainRejection(ctx context.Context, in ClaimInput) error {
	var open bool
	var maxPerBuyer int
	err := s.pool.QueryRow(ctx, `
		SELECT now() >= opens_at AND now() < closes_at, max_per_buyer
		  FROM drops WHERE id = $1`, in.DropID).Scan(&open, &maxPerBuyer)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return ErrNotFound
	case err != nil:
		return err
	case !open:
		return ErrNotOpen
	case in.Qty > maxPerBuyer:
		return ErrQtyExceedsLimit
	default:
		return ErrSoldOut
	}
}

func (s *Store) claimByKey(ctx context.Context, dropID, key string) (Claim, error) {
	return scanClaim(s.pool.QueryRow(ctx,
		`SELECT `+claimCols+` FROM claims WHERE drop_id = $1 AND idempotency_key = $2`, dropID, key))
}

func replay(existing Claim, in ClaimInput) (Claim, bool, error) {
	if existing.BuyerRef != in.BuyerRef || existing.Qty != in.Qty {
		return Claim{}, false, ErrIdempotencyMismatch
	}
	return existing, true, nil
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}
