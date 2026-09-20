package main

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/jmoiron/sqlx"
)

// SentMarkBuffer は ride_statuses の送信済みマーカー
// （app_sent_at / chair_sent_at）の書込みバッファ。
// SSE配送のたびに発行されていた単発UPDATE（DB CPUの約半分）を、
// 短周期の multi-row UPDATE に束ねる。等価性の根拠:
//   - マーカー列の実行時読手は存在しない（SSE未送信の追跡は
//     StatusLog がインメモリで行う）。DB値は起動時・初期化時の
//     reloadStatusLog（未送信行の復元）のみが読む。
//   - バルクUPDATEは単発版の束と等価（NULL行のみ更新・冪等）。
//     flush遅延分だけ復元対象に残るが、再起動は走行中に起きないため無害。
//   - 失敗時は滞留分を戻して次tickで再試行する（欠損させない）。
//   - 終了時は main の shutdown 処理で Flush する。
//   - postInitialize（DB全初期化）時は Discard で破棄する。
//     （テーブル自体が作り直されるため、旧行のマークはゴミになる）
type SentMarkBuffer struct {
	db       *sqlx.DB
	mu       sync.Mutex
	appIDs   map[string]struct{}
	chairIDs map[string]struct{}
	interval time.Duration
	// execApp/execChair は flush の書込み処理。テストで差し替え可能にする。
	execApp   func(ctx context.Context, ids []string) error
	execChair func(ctx context.Context, ids []string) error
}

func NewSentMarkBuffer(db *sqlx.DB) *SentMarkBuffer {
	b := &SentMarkBuffer{
		db:       db,
		appIDs:   make(map[string]struct{}),
		chairIDs: make(map[string]struct{}),
		interval: time.Second,
	}
	b.execApp = func(ctx context.Context, ids []string) error {
		return rideStatusRepository.MarkAppSentBulk(ctx, b.db, ids)
	}
	b.execChair = func(ctx context.Context, ids []string) error {
		return rideStatusRepository.MarkChairSentBulk(ctx, b.db, ids)
	}
	return b
}

// MarkApp はユーザー向け送達を記録する。DB書込は flush 時に束ねる。
func (b *SentMarkBuffer) MarkApp(id string) {
	b.mu.Lock()
	b.appIDs[id] = struct{}{}
	b.mu.Unlock()
}

// MarkChair は椅子向け送達を記録する。DB書込は flush 時に束ねる。
func (b *SentMarkBuffer) MarkChair(id string) {
	b.mu.Lock()
	b.chairIDs[id] = struct{}{}
	b.mu.Unlock()
}

// Discard は滞留分を破棄する。postInitialize（DB全初期化）用。
func (b *SentMarkBuffer) Discard() {
	b.mu.Lock()
	b.appIDs = make(map[string]struct{})
	b.chairIDs = make(map[string]struct{})
	b.mu.Unlock()
}

func (b *SentMarkBuffer) Start(ctx context.Context) {
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

// Flush は滞留分を宛先別の multi-row UPDATE（最大2本）で書込む。
// 失敗時は滞留分を戻して次tickで再試行する（欠損させない）。
func (b *SentMarkBuffer) Flush(ctx context.Context) {
	b.mu.Lock()
	if len(b.appIDs) == 0 && len(b.chairIDs) == 0 {
		b.mu.Unlock()
		return
	}
	appIDs := keys(b.appIDs)
	chairIDs := keys(b.chairIDs)
	b.appIDs = make(map[string]struct{})
	b.chairIDs = make(map[string]struct{})
	b.mu.Unlock()

	if err := b.execApp(ctx, appIDs); err != nil {
		slog.Error("failed to flush app sent marks", "error", err, "rows", len(appIDs))
		b.requeue(appIDs, true)
		b.requeue(chairIDs, false)
		return
	}
	if err := b.execChair(ctx, chairIDs); err != nil {
		slog.Error("failed to flush chair sent marks", "error", err, "rows", len(chairIDs))
		b.requeue(chairIDs, false)
		return
	}
}

func (b *SentMarkBuffer) requeue(ids []string, app bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, id := range ids {
		if app {
			b.appIDs[id] = struct{}{}
		} else {
			b.chairIDs[id] = struct{}{}
		}
	}
}

func keys(set map[string]struct{}) []string {
	out := make([]string, 0, len(set))
	for id := range set {
		out = append(out, id)
	}
	return out
}
