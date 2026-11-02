package events

import (
	"context"
	"log/slog"
	"os"
	"testing"
	"time"
)

// Needs a real MongoDB:
//
//	docker compose up -d mongo
//	TEST_MONGODB_URI="mongodb://localhost:27018" go test -race -count=1 ./internal/events/
func TestMongoRecordAndRecent(t *testing.T) {
	uri := os.Getenv("TEST_MONGODB_URI")
	if uri == "" {
		t.Skip("TEST_MONGODB_URI not set")
	}
	ctx := context.Background()
	rec, err := New(ctx, uri, "dropgate_test", slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	defer rec.Close(ctx) //nolint:errcheck

	drop := "test-drop-" + time.Now().Format("150405.000000")
	rec.Record(Event{Type: ClaimCreated, DropID: drop, BuyerRef: "a", Data: map[string]any{"qty": 1}})
	rec.Record(Event{Type: ClaimRejected, DropID: drop, BuyerRef: "b", Data: map[string]any{"reason": "SOLD_OUT"}})
	rec.Record(Event{Type: ClaimReplayed, DropID: drop, BuyerRef: "a"})

	// Writes are async and batched, so poll instead of reading once.
	deadline := time.Now().Add(3 * time.Second)
	for {
		got, err := rec.Recent(ctx, drop, 10)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) == 3 {
			if got[0].At.Before(got[2].At) {
				t.Fatal("events should be newest first")
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("want 3 events, got %d", len(got))
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestNoopIsDisabled(t *testing.T) {
	var r Recorder = Noop{}
	r.Record(Event{Type: ClaimCreated}) // must not panic
	if _, err := r.Recent(context.Background(), "x", 1); err != ErrDisabled {
		t.Fatalf("want ErrDisabled, got %v", err)
	}
}
