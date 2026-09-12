package repository

import (
	"context"

	"github.com/jmoiron/sqlx"

	"github.com/isucon/isucon14/webapp/go/models"
)

type CouponRepository struct {
	db *sqlx.DB
}

func NewCouponRepository(db *sqlx.DB) *CouponRepository {
	return &CouponRepository{db: db}
}

// ListByUsedByIDs は指定ライドに紐づくクーポンを一括取得する。履歴表示の N+1 解消用。
func (r *CouponRepository) ListByUsedByIDs(ctx context.Context, q Selecter, rideIDs []string) ([]models.Coupon, error) {
	coupons := []models.Coupon{}
	if len(rideIDs) == 0 {
		return coupons, nil
	}
	// IN (?) にはスライスを1引数で渡す（スカラー展開渡しは余剰引数エラーになる）
	query, params, err := sqlx.In(`SELECT * FROM coupons WHERE used_by IN (?)`, rideIDs)
	if err != nil {
		return nil, err
	}
	if err := q.SelectContext(ctx, &coupons, sqlx.Rebind(sqlx.QUESTION, query), params...); err != nil {
		return nil, err
	}
	return coupons, nil
}
