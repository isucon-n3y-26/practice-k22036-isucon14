package cache

import (
	"context"
	"sync"
)

// RideCoords は遷移判定に必要なライドの不変フィールド。
// pickup/destination/user は作成後不変のため、無効化不要でキャッシュできる。
type RideCoords struct {
	RideID               string
	UserID               string
	PickupLatitude       int
	PickupLongitude      int
	DestinationLatitude  int
	DestinationLongitude int
}

// RideCoordsLoader はキャッシュmiss時のフォールバック取得。呼び出し側で注入する。
type RideCoordsLoader func(ctx context.Context, rideID string) (RideCoords, error)

// RideCoordsCache はライド座標のインメモリキャッシュ。
// 保持フィールドが不変のため、明示的な無効化は不要。
// 複数台構成にする場合は使用できない（単一インスタンス前提）。
type RideCoordsCache struct {
	mu   sync.RWMutex
	m    map[string]RideCoords
	load RideCoordsLoader
}

func NewRideCoordsCache(load RideCoordsLoader) *RideCoordsCache {
	return &RideCoordsCache{m: make(map[string]RideCoords), load: load}
}

// Get は座標を返す。未キャッシュ時はロードして載せる。
func (c *RideCoordsCache) Get(ctx context.Context, rideID string) (RideCoords, error) {
	c.mu.RLock()
	coords, ok := c.m[rideID]
	c.mu.RUnlock()
	if ok {
		return coords, nil
	}

	coords, err := c.load(ctx, rideID)
	if err != nil {
		return RideCoords{}, err
	}

	c.mu.Lock()
	c.m[rideID] = coords
	c.mu.Unlock()
	return coords, nil
}

// Clear はDB初期化時（全データ破棄時）に呼ぶこと。
func (c *RideCoordsCache) Clear() {
	c.mu.Lock()
	c.m = make(map[string]RideCoords)
	c.mu.Unlock()
}
