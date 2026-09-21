package main

import (
	"context"
	"database/sql"
	"errors"
	"sync"
)

// discountCache はライドに紐づくクーポン割引額の read-through。
// 割引の確定は配車要求TX内の ClaimByCode のみで、以後そのライドの
// 割引額は不変のため、確定後の seed/read-through はDB読取りと等価。
// 運賃計算（通知・評価・見積）の coupon 都度SELECT排除用。
type discountCache struct {
	m sync.Map // rideID -> int
}

var globalDiscountCache = &discountCache{}

// Get はライドの割引額を返す。未ヒット時はDBから読んで保持する。
func (c *discountCache) Get(ctx context.Context, rideID string) (int, error) {
	if v, ok := c.m.Load(rideID); ok {
		return v.(int), nil
	}
	coupon, err := couponRepository.GetByUsedBy(ctx, db, rideID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			c.m.Store(rideID, 0)
			return 0, nil
		}
		return 0, err
	}
	c.m.Store(rideID, coupon.Discount)
	return coupon.Discount, nil
}

// Set はクーポン確定コミット直後に割引額を記録する。
func (c *discountCache) Set(rideID string, discount int) {
	c.m.Store(rideID, discount)
}

// Clear はキャッシュを破棄する。初期化でDBが全消去されるため。
func (c *discountCache) Clear() {
	c.m = sync.Map{}
}
