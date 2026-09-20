package main

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/jmoiron/sqlx"

	"github.com/isucon/isucon14/webapp/go/models"
)

// RideStatusBuffer は ride_statuses の遷移行INSERTを束ねる書込みバッファ。
// 受諾・出発・到着など1ライド約5行の確定INSERTを、短周期の multi-row INSERT
// に束ねる。COMPLETEDだけは対象外（履歴表示と売上集計が最新status行を
// DBから直接読むため、同期INSERTする）。等価性の根拠:
//   - バッファ対象遷移の実行時読手は StatusCache（インメモリ）のみで、
//     遷移確定と同時に Set されるため、DB反映遅延は観測されない。
//     DB行を読むのは起動時・初期化時の復元系と、遷移を含まない履歴表示のみ。
//   - created_at は遷移時刻を明示挿入するため、順序・時刻の意味は不変。
//   - 失敗時は先頭に戻して次tickで再試行する（欠損させない。順序も保つ）。
//   - 終了時は main の shutdown 処理で Flush する。
//   - postInitialize（DB全初期化）時は Discard で破棄する。
//     （テーブル自体が作り直されるため、旧行の flush はゴミになる）
type RideStatusBuffer struct {
	db       *sqlx.DB
	mu       sync.Mutex
	pending  []models.RideStatus
	interval time.Duration
	batchMax int
	// exec は flush の書込み処理。テストで差し替え可能にする。
	exec func(ctx context.Context, rows []models.RideStatus) error
}

func NewRideStatusBuffer(db *sqlx.DB) *RideStatusBuffer {
	b := &RideStatusBuffer{
		db:       db,
		interval: time.Second,
		batchMax: 1000,
	}
	b.exec = func(ctx context.Context, rows []models.RideStatus) error {
		return rideStatusRepository.BulkCreate(ctx, b.db, rows)
	}
	return b
}

// Append は遷移行を滞留させる。created_at は呼出し側で遷移時刻を入れること。
func (b *RideStatusBuffer) Append(row models.RideStatus) {
	b.mu.Lock()
	b.pending = append(b.pending, row)
	b.mu.Unlock()
}

// Discard は滞留分を破棄する。postInitialize（DB全初期化）用。
func (b *RideStatusBuffer) Discard() {
	b.mu.Lock()
	b.pending = nil
	b.mu.Unlock()
}

func (b *RideStatusBuffer) Start(ctx context.Context) {
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
// 失敗時は先頭に戻して次tickで再試行する（欠損させない）。
func (b *RideStatusBuffer) Flush(ctx context.Context) {
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
		if err := b.exec(ctx, chunk); err != nil {
			slog.Error("failed to flush ride statuses", "error", err, "rows", len(chunk))
			b.mu.Lock()
			b.pending = append(chunk, append(batch, b.pending...)...)
			b.mu.Unlock()
			return
		}
	}
}
