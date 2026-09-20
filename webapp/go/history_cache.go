package main

import (
	"sync"
)

// UserHistoryCache はユーザー別ライド履歴（appGetRides 応答の中身）の
// インメモリキャッシュ。履歴表示のDB読取（完了ライド＋クーポン＋椅子＋
// オーナーの最大4クエリ）を削減する。等価性の根拠:
//   - 完了済みライド行は不変（評価は1回のみで二重評価は400、
//     椅子割当・クーポン紐付けは完了前に確定）。
//     履歴に含まれる運賃・評価・完了時刻・椅子情報は完了後に変わらない。
//   - 履歴が変わるのはそのユーザーの評価確定時のみで、
//     評価ハンドラのコミット直後に無効化する（同一プロセス・単一app前提）。
//   - postInitialize（DB全初期化）時は Clear で破棄する。
type UserHistoryCache struct {
	mu sync.RWMutex
	m  map[string][]getAppRidesResponseItem
}

func NewUserHistoryCache() *UserHistoryCache {
	return &UserHistoryCache{m: make(map[string][]getAppRidesResponseItem)}
}

// Get は履歴を返す。未キャッシュ時は ok=false。
func (c *UserHistoryCache) Get(userID string) (items []getAppRidesResponseItem, ok bool) {
	c.mu.RLock()
	items, ok = c.m[userID]
	c.mu.RUnlock()
	return items, ok
}

// Set は履歴を載せる。空履歴も載せる（新規ユーザーの初回参照対策）。
func (c *UserHistoryCache) Set(userID string, items []getAppRidesResponseItem) {
	c.mu.Lock()
	c.m[userID] = items
	c.mu.Unlock()
}

// Invalidate は指定ユーザーの履歴を破棄する。評価確定時に呼ぶこと。
func (c *UserHistoryCache) Invalidate(userID string) {
	c.mu.Lock()
	delete(c.m, userID)
	c.mu.Unlock()
}

// Clear は全破棄する。postInitialize（DB全初期化）用。
func (c *UserHistoryCache) Clear() {
	c.mu.Lock()
	c.m = make(map[string][]getAppRidesResponseItem)
	c.mu.Unlock()
}
