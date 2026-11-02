package store

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ansrazm/dropgate/internal/db"
)

// These tests need a real Postgres:
//
//	docker compose up -d db
//	TEST_DATABASE_URL="postgres://dropgate:dropgate@localhost:5434/dropgate?sslmode=disable" go test -race -count=1 ./...
func newTestStore(t *testing.T) (*Store, *pgxpool.Pool) {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.MaxConns = 20
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err := db.Migrate(context.Background(), pool); err != nil {
		t.Fatal(err)
	}
	return New(pool), pool
}

func newOpenDrop(t *testing.T, s *Store, total, maxPerBuyer int) Drop {
	t.Helper()
	d, err := s.CreateDrop(context.Background(), CreateDropInput{
		Title: t.Name(), TotalQty: total, PriceIDR: 100000,
		MaxPerBuyer: maxPerBuyer, HoldSeconds: 600,
		OpensAt: time.Now().Add(-time.Minute), ClosesAt: time.Now().Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// assertInvariant checks the one rule the whole design exists to protect:
// remaining + units held or confirmed == total, always.
func assertInvariant(t *testing.T, pool *pgxpool.Pool, dropID string) (remaining int) {
	t.Helper()
	var total, rem int
	var held int64
	err := pool.QueryRow(context.Background(), `
		SELECT d.total_qty, d.remaining,
		       COALESCE(SUM(c.qty) FILTER (WHERE c.status IN ('HELD','CONFIRMED')), 0)
		  FROM drops d LEFT JOIN claims c ON c.drop_id = d.id
		 WHERE d.id = $1 GROUP BY d.id`, dropID).Scan(&total, &rem, &held)
	if err != nil {
		t.Fatal(err)
	}
	if int64(rem)+held != int64(total) {
		t.Fatalf("invariant broken: remaining=%d held+confirmed=%d total=%d", rem, held, total)
	}
	return rem
}

func TestConcurrentClaims_NoOversell(t *testing.T) {
	s, pool := newTestStore(t)
	d := newOpenDrop(t, s, 10, 1)

	const buyers = 200
	var ok, soldOut atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < buyers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, _, err := s.CreateClaim(context.Background(), ClaimInput{
				DropID: d.ID, BuyerRef: fmt.Sprintf("buyer-%d", i), Qty: 1,
				IdempotencyKey: fmt.Sprintf("key-%d", i),
			})
			switch {
			case err == nil:
				ok.Add(1)
			case errors.Is(err, ErrSoldOut):
				soldOut.Add(1)
			default:
				t.Errorf("unexpected error: %v", err)
			}
		}(i)
	}
	wg.Wait()

	if ok.Load() != 10 || soldOut.Load() != buyers-10 {
		t.Fatalf("want 10 ok / %d sold out, got %d / %d", buyers-10, ok.Load(), soldOut.Load())
	}
	if rem := assertInvariant(t, pool, d.ID); rem != 0 {
		t.Fatalf("want remaining 0, got %d", rem)
	}
}

func TestIdempotentReplay_SameKeyTakesStockOnce(t *testing.T) {
	s, pool := newTestStore(t)
	d := newOpenDrop(t, s, 10, 1)

	const attempts = 8
	ids := make(chan string, attempts)
	var replays atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, replayed, err := s.CreateClaim(context.Background(), ClaimInput{
				DropID: d.ID, BuyerRef: "same-buyer", Qty: 1, IdempotencyKey: "same-key",
			})
			if err != nil {
				t.Errorf("unexpected error: %v", err)
				return
			}
			if replayed {
				replays.Add(1)
			}
			ids <- c.ID
		}()
	}
	wg.Wait()
	close(ids)

	first := ""
	for id := range ids {
		if first == "" {
			first = id
		}
		if id != first {
			t.Fatalf("got different claim ids for one key: %s vs %s", first, id)
		}
	}
	if replays.Load() != attempts-1 {
		t.Fatalf("want %d replays, got %d", attempts-1, replays.Load())
	}
	if rem := assertInvariant(t, pool, d.ID); rem != 9 {
		t.Fatalf("want remaining 9, got %d", rem)
	}
}

func TestOneActiveClaimPerBuyer_RollsBackStock(t *testing.T) {
	s, pool := newTestStore(t)
	d := newOpenDrop(t, s, 10, 1)
	ctx := context.Background()

	if _, _, err := s.CreateClaim(ctx, ClaimInput{DropID: d.ID, BuyerRef: "ani", Qty: 1, IdempotencyKey: "k1"}); err != nil {
		t.Fatal(err)
	}
	_, _, err := s.CreateClaim(ctx, ClaimInput{DropID: d.ID, BuyerRef: "ani", Qty: 1, IdempotencyKey: "k2"})
	if !errors.Is(err, ErrAlreadyClaimed) {
		t.Fatalf("want ErrAlreadyClaimed, got %v", err)
	}
	// the failed attempt decremented stock inside its transaction; it must be undone
	if rem := assertInvariant(t, pool, d.ID); rem != 9 {
		t.Fatalf("want remaining 9, got %d", rem)
	}
}

func TestIdempotencyKeyReusedWithDifferentRequest(t *testing.T) {
	s, _ := newTestStore(t)
	d := newOpenDrop(t, s, 10, 2)
	ctx := context.Background()

	if _, _, err := s.CreateClaim(ctx, ClaimInput{DropID: d.ID, BuyerRef: "ani", Qty: 1, IdempotencyKey: "k1"}); err != nil {
		t.Fatal(err)
	}
	_, _, err := s.CreateClaim(ctx, ClaimInput{DropID: d.ID, BuyerRef: "ani", Qty: 2, IdempotencyKey: "k1"})
	if !errors.Is(err, ErrIdempotencyMismatch) {
		t.Fatalf("want ErrIdempotencyMismatch, got %v", err)
	}
}

func TestRejections(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()

	future, err := s.CreateDrop(ctx, CreateDropInput{
		Title: "future", TotalQty: 5, MaxPerBuyer: 1, HoldSeconds: 600,
		OpensAt: time.Now().Add(time.Hour), ClosesAt: time.Now().Add(2 * time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	open := newOpenDrop(t, s, 5, 1)

	cases := []struct {
		name string
		in   ClaimInput
		want error
	}{
		{"not open yet", ClaimInput{DropID: future.ID, BuyerRef: "a", Qty: 1, IdempotencyKey: "n1"}, ErrNotOpen},
		{"over per-buyer limit", ClaimInput{DropID: open.ID, BuyerRef: "a", Qty: 2, IdempotencyKey: "n2"}, ErrQtyExceedsLimit},
		{"unknown drop", ClaimInput{DropID: "00000000-0000-0000-0000-000000000000", BuyerRef: "a", Qty: 1, IdempotencyKey: "n3"}, ErrNotFound},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, _, err := s.CreateClaim(ctx, c.in); !errors.Is(err, c.want) {
				t.Fatalf("want %v, got %v", c.want, err)
			}
		})
	}
}
