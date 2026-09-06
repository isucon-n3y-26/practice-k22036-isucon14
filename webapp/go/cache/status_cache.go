// Package cache はインメモリキャッシュを提供する。
// main パッケージへの依存を持たないため、循環参照は発生しない。
package cache

import (
	"context"
	"sync"
)

// Getter は単一行取得のための最小インターフェース。
// *sqlx.DB / *sqlx.Tx のいずれも満たす。
type Getter interface {
	GetContext(ctx context.Context, dest any, query string, args ...any) error
}

// FetchFunc はキャッシュmiss時のフォールバック取得。呼び出し側で注入する。
type FetchFunc func(ctx context.Context, q Getter, rideID string) (string, error)

// StatusCache はライド最新状態のインメモリキャッシュ。
// ride_statuses への全書き込み元が同一プロセス内に限定される前提で、
// 各コミット直後の Set によりDBと乖離しない。
// 未キャッシュ時は fetch にフォールバックして載せる read-through のため、
// 万一の取りこぼしは遅延に留まり誤動作しない。
// 複数台構成にする場合は使用できない（単一インスタンス前提）。
type StatusCache struct {
	mu    sync.RWMutex
	m     map[string]string
	fetch FetchFunc
}

func NewStatusCache(fetch FetchFunc) *StatusCache {
	return &StatusCache{m: make(map[string]string), fetch: fetch}
}

// Get は最新状態を返す。トランザクション内からの呼び出しにも対応するが、
// 呼び出し側トランザクション内での未コミット書き込みより前に読むこと。
func (c *StatusCache) Get(ctx context.Context, q Getter, rideID string) (string, error) {
	c.mu.RLock()
	status, ok := c.m[rideID]
	c.mu.RUnlock()
	if ok {
		return status, nil
	}

	status, err := c.fetch(ctx, q, rideID)
	if err != nil {
		return "", err
	}

	c.mu.Lock()
	c.m[rideID] = status
	c.mu.Unlock()
	return status, nil
}

// Set は状態遷移のコミット直後に呼ぶこと。ロールバック時は呼ばない。
func (c *StatusCache) Set(rideID, status string) {
	c.mu.Lock()
	c.m[rideID] = status
	c.mu.Unlock()
}

// Clear はDB初期化時（全データ破棄時）に呼ぶこと。
func (c *StatusCache) Clear() {
	c.mu.Lock()
	c.m = make(map[string]string)
	c.mu.Unlock()
}
