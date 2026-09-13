package main

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/jmoiron/sqlx"
)

// locationEntry は chair_locations への後追いINSERT待ち1行。
type locationEntry struct {
	ID        string
	ChairID   string
	Latitude  int
	Longitude int
	CreatedAt time.Time
}

// LocationBuffer は座標POSTの最高頻度INSERTをリクエストパスから外すための
// 書込みバッファ。追い越し読みは存在しない（最新位置は ChairManager が保持し、
// chair_locations の実行時読取りは起動時Reloadのみ）のため、短周期の
// multi-row flush で等価。created_at は enqueue 時刻を明示挿入する。
type LocationBuffer struct {
	db       *sqlx.DB
	mu       sync.Mutex
	pending  []locationEntry
	interval time.Duration
	batchMax int
}

func NewLocationBuffer(db *sqlx.DB) *LocationBuffer {
	return &LocationBuffer{
		db:       db,
		interval: 100 * time.Millisecond,
		batchMax: 1000,
	}
}

func (b *LocationBuffer) Append(e locationEntry) {
	b.mu.Lock()
	b.pending = append(b.pending, e)
	b.mu.Unlock()
}

// Discard は滞留分を破棄する。postInitialize（DB全初期化）用。
// 初期化でテーブル自体が破棄されるため、旧行の flush はゴミになる。
func (b *LocationBuffer) Discard() {
	b.mu.Lock()
	b.pending = nil
	b.mu.Unlock()
}

func (b *LocationBuffer) Start(ctx context.Context) {
	ticker := time.NewTicker(b.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			b.Flush(context.Background())
		}
	}
}

// Flush は滞留分を batchMax 行ずつの multi-row INSERT で書込む。
// 失敗時は先頭に戻して次 tick で再試行する（欠損させない）。
func (b *LocationBuffer) Flush(ctx context.Context) {
	b.mu.Lock()
	if len(b.pending) == 0 {
		b.mu.Unlock()
		return
	}
	batch := b.pending
	b.pending = nil
	b.mu.Unlock()

	for len(batch) > 0 {
		n := min(len(batch), b.batchMax)
		chunk := batch[:n]
		batch = batch[n:]
		if err := insertLocationChunk(ctx, b.db, chunk); err != nil {
			slog.Error("failed to flush chair locations", "error", err, "rows", len(chunk))
			b.mu.Lock()
			b.pending = append(chunk, append(batch, b.pending...)...)
			b.mu.Unlock()
			return
		}
	}
}

func insertLocationChunk(ctx context.Context, db *sqlx.DB, chunk []locationEntry) error {
	var sb strings.Builder
	sb.WriteString(`INSERT INTO chair_locations (id, chair_id, latitude, longitude, created_at) VALUES `)
	args := make([]any, 0, len(chunk)*5)
	for i, e := range chunk {
		if i > 0 {
			sb.WriteString(",")
		}
		sb.WriteString("(?,?,?,?,?)")
		args = append(args, e.ID, e.ChairID, e.Latitude, e.Longitude, e.CreatedAt)
	}
	_, err := db.ExecContext(ctx, sb.String(), args...)
	return err
}
