package repository

import (
	"context"
	"strconv"
	"time"

	"github.com/jmoiron/sqlx"

	"github.com/isucon/isucon14/webapp/go/models"
)

type CouponRepository struct {
	db *sqlx.DB
}

func NewCouponRepository(db *sqlx.DB) *CouponRepository {
	return &CouponRepository{db: db}
}

// GetByUsedBy はライドに紐づくクーポンを返す。未使用時は sql.ErrNoRows。
// 運賃計算で割引額参照用。
func (r *CouponRepository) GetByUsedBy(ctx context.Context, q Getter, rideID string) (*models.Coupon, error) {
	coupon := &models.Coupon{}
	if err := q.GetContext(ctx, coupon,
		"SELECT * FROM coupons WHERE used_by = ?",
		rideID,
	); err != nil {
		return nil, err
	}
	return coupon, nil
}

// GetUnusedNewUserCoupon は未使用の初回利用クーポンを返す。無い時は sql.ErrNoRows。
func (r *CouponRepository) GetUnusedNewUserCoupon(ctx context.Context, q Getter, userID string) (*models.Coupon, error) {
	coupon := &models.Coupon{}
	if err := q.GetContext(ctx, coupon,
		"SELECT * FROM coupons WHERE user_id = ? AND code = 'CP_NEW2024' AND used_by IS NULL",
		userID,
	); err != nil {
		return nil, err
	}
	return coupon, nil
}

// GetOldestUnused は未使用クーポンを付与順で1件返す。無い時は sql.ErrNoRows。
func (r *CouponRepository) GetOldestUnused(ctx context.Context, q Getter, userID string) (*models.Coupon, error) {
	coupon := &models.Coupon{}
	if err := q.GetContext(ctx, coupon,
		"SELECT * FROM coupons WHERE user_id = ? AND used_by IS NULL ORDER BY created_at LIMIT 1",
		userID,
	); err != nil {
		return nil, err
	}
	return coupon, nil
}

// GetNewUserCouponForUpdate は未使用の初回利用クーポンをロック付きで返す。
// 配車時の確定用。無い時は sql.ErrNoRows。
func (r *CouponRepository) GetNewUserCouponForUpdate(ctx context.Context, q Getter, userID string) (*models.Coupon, error) {
	coupon := &models.Coupon{}
	if err := q.GetContext(ctx, coupon,
		"SELECT * FROM coupons WHERE user_id = ? AND code = 'CP_NEW2024' AND used_by IS NULL FOR UPDATE",
		userID,
	); err != nil {
		return nil, err
	}
	return coupon, nil
}

// GetOldestUnusedForUpdate は未使用クーポンを付与順でロック付きで1件返す。
// 配車時の確定用。無い時は sql.ErrNoRows。
func (r *CouponRepository) GetOldestUnusedForUpdate(ctx context.Context, q Getter, userID string) (*models.Coupon, error) {
	coupon := &models.Coupon{}
	if err := q.GetContext(ctx, coupon,
		"SELECT * FROM coupons WHERE user_id = ? AND used_by IS NULL ORDER BY created_at LIMIT 1 FOR UPDATE",
		userID,
	); err != nil {
		return nil, err
	}
	return coupon, nil
}

// ClaimByCode は指定クーポンをライドに紐付けて確定する。
// 未使用行のみ対象とし、異常時の上書き付け替えを防ぐ。
func (r *CouponRepository) ClaimByCode(ctx context.Context, q Queryer, rideID, userID, code string) error {
	_, err := q.ExecContext(
		ctx,
		"UPDATE coupons SET used_by = ? WHERE user_id = ? AND code = ? AND used_by IS NULL",
		rideID, userID, code,
	)
	return err
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

func (r *CouponRepository) Create(ctx context.Context, q Queryer, coupon *models.Coupon) error {
	_, err := q.ExecContext(
		ctx,
		"INSERT INTO coupons (user_id, code, discount) VALUES (?, ?, ?)",
		coupon.UserID, coupon.Code, coupon.Discount,
	)
	return err
}

// CountByCodeForUpdate は指定コードのクーポン件数を返す。招待上限チェック用で、
// 直列化のため FOR UPDATE を維持する（取得は SELECT 1 のみで件数は len で数える）。
func (r *CouponRepository) CountByCodeForUpdate(ctx context.Context, q Selecter, code string) (int, error) {
	var ones []int
	if err := q.SelectContext(ctx, &ones, "SELECT 1 FROM coupons WHERE code = ? FOR UPDATE", code); err != nil {
		return 0, err
	}
	return len(ones), nil
}

// CreateInvitationPair は招待クーポンと招待者Rewardを1文で付与する。
// Rewardコード末尾のミリ秒サフィックスはGo側で生成する
// （元は FLOOR(UNIX_TIMESTAMP(NOW(3))*1000) と等価）。
func (r *CouponRepository) CreateInvitationPair(ctx context.Context, q Queryer, userID, invitationCode, inviterID string) error {
	rewardCode := "RWD_" + invitationCode + "_" + strconv.FormatInt(time.Now().UnixMilli(), 10)
	_, err := q.ExecContext(
		ctx,
		"INSERT INTO coupons (user_id, code, discount) VALUES (?, ?, ?), (?, ?, ?)",
		userID, "INV_"+invitationCode, 1500,
		inviterID, rewardCode, 1000,
	)
	return err
}
