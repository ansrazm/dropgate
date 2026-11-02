// Package events is the append-only event log. PostgreSQL stays the source of
// truth for stock and claims; this package only records what happened, so it
// must never be able to fail or slow down a claim.
package events

import (
	"context"
	"errors"
	"log/slog"
	"time"
)

// Event types written by the API.
const (
	ClaimCreated  = "claim.created"
	ClaimReplayed = "claim.replayed"
	ClaimRejected = "claim.rejected"
)

// Event is one thing that happened. Data differs per event type (a created
// claim has claim_id and expires_at, a rejection has reason), which is the
// reason this lives in a document store with a flexible shape.
type Event struct {
	Type      string         `bson:"type" json:"type"`
	DropID    string         `bson:"drop_id" json:"drop_id"`
	BuyerRef  string         `bson:"buyer_ref,omitempty" json:"buyer_ref,omitempty"`
	RequestID string         `bson:"request_id,omitempty" json:"request_id,omitempty"`
	At        time.Time      `bson:"at" json:"at"`
	Data      map[string]any `bson:"data,omitempty" json:"data,omitempty"`
}

// ErrDisabled is returned by reads when no MongoDB is configured.
var ErrDisabled = errors.New("event log disabled (MONGODB_URI not set)")

// Recorder is best-effort by design: Record never blocks and never returns an
// error. If the log is slow or down, events are dropped and counted, because a
// claim must not fail just because its log entry could not be written.
type Recorder interface {
	Record(Event)
	Recent(ctx context.Context, dropID string, limit int) ([]Event, error)
	Close(ctx context.Context) error
}

// New returns a MongoDB-backed Recorder, or a no-op one when uri is empty so
// the API still runs with only PostgreSQL.
func New(ctx context.Context, uri, dbName string, log *slog.Logger) (Recorder, error) {
	if uri == "" {
		log.Info("event log disabled", "reason", "MONGODB_URI not set")
		return Noop{}, nil
	}
	return newMongo(ctx, uri, dbName, log)
}

// Noop discards events.
type Noop struct{}

func (Noop) Record(Event) {}
func (Noop) Recent(context.Context, string, int) ([]Event, error) {
	return nil, ErrDisabled
}
func (Noop) Close(context.Context) error { return nil }
