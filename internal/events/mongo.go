package events

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"go.mongodb.org/mongo-driver/v2/mongo/readpref"
)

const (
	queueSize     = 1024
	batchSize     = 100
	flushInterval = 250 * time.Millisecond
	retention     = 30 * 24 * time.Hour
)

type mongoRecorder struct {
	client  *mongo.Client
	coll    *mongo.Collection
	log     *slog.Logger
	ch      chan Event
	done    chan struct{}
	dropped atomic.Int64

	mu     sync.RWMutex // guards closed and sends on ch
	closed bool
}

func newMongo(ctx context.Context, uri, dbName string, log *slog.Logger) (Recorder, error) {
	client, err := mongo.Connect(options.Client().ApplyURI(uri))
	if err != nil {
		return nil, err
	}
	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := client.Ping(pingCtx, readpref.Primary()); err != nil {
		_ = client.Disconnect(context.Background())
		return nil, err
	}

	coll := client.Database(dbName).Collection("events")
	_, err = coll.Indexes().CreateMany(pingCtx, []mongo.IndexModel{
		// "latest events of a drop" is the one read pattern.
		{Keys: bson.D{{Key: "drop_id", Value: 1}, {Key: "at", Value: -1}}},
		// A log should not grow forever: Mongo deletes old events itself.
		{
			Keys:    bson.D{{Key: "at", Value: 1}},
			Options: options.Index().SetExpireAfterSeconds(int32(retention / time.Second)),
		},
	})
	if err != nil {
		_ = client.Disconnect(context.Background())
		return nil, err
	}

	m := &mongoRecorder{
		client: client, coll: coll, log: log,
		ch: make(chan Event, queueSize), done: make(chan struct{}),
	}
	go m.run()
	log.Info("event log enabled", "db", dbName, "collection", "events")
	return m, nil
}

// Record enqueues without blocking. A full queue drops the event.
func (m *mongoRecorder) Record(ev Event) {
	if ev.At.IsZero() {
		ev.At = time.Now().UTC()
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.closed {
		return
	}
	select {
	case m.ch <- ev:
	default:
		if n := m.dropped.Add(1); n == 1 || n%1000 == 0 {
			m.log.Warn("event queue full, dropping events", "dropped_total", n)
		}
	}
}

// run batches events so a burst (200 buyers at once) becomes a few inserts.
func (m *mongoRecorder) run() {
	defer close(m.done)
	batch := make([]any, 0, batchSize)
	tick := time.NewTicker(flushInterval)
	defer tick.Stop()

	flush := func() {
		if len(batch) == 0 {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := m.coll.InsertMany(ctx, batch); err != nil {
			m.log.Error("event insert failed", "err", err, "events", len(batch))
		}
		batch = make([]any, 0, batchSize)
	}

	for {
		select {
		case ev, ok := <-m.ch:
			if !ok {
				flush()
				return
			}
			batch = append(batch, ev)
			if len(batch) >= batchSize {
				flush()
			}
		case <-tick.C:
			flush()
		}
	}
}

func (m *mongoRecorder) Recent(ctx context.Context, dropID string, limit int) ([]Event, error) {
	if limit < 1 || limit > 200 {
		limit = 50
	}
	cur, err := m.coll.Find(ctx, bson.D{{Key: "drop_id", Value: dropID}},
		options.Find().SetSort(bson.D{{Key: "at", Value: -1}}).SetLimit(int64(limit)))
	if err != nil {
		return nil, err
	}
	out := []Event{}
	if err := cur.All(ctx, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// Close flushes what is queued, then disconnects.
func (m *mongoRecorder) Close(ctx context.Context) error {
	m.mu.Lock()
	if !m.closed {
		m.closed = true
		close(m.ch)
	}
	m.mu.Unlock()

	select {
	case <-m.done:
	case <-ctx.Done():
	}
	return m.client.Disconnect(ctx)
}
