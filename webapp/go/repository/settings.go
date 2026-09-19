package repository

import (
	"context"

	"github.com/jmoiron/sqlx"
)

// SettingsRepository は設定テーブルへのアクセスを集約する。
// payment_gateway_url は起動時・初期化時にのみ読み書きされる。
type SettingsRepository struct {
	db *sqlx.DB
}

func NewSettingsRepository(db *sqlx.DB) *SettingsRepository {
	return &SettingsRepository{db: db}
}

// GetPaymentGatewayURL は決済サーバのURLを返す。
func (r *SettingsRepository) GetPaymentGatewayURL(ctx context.Context, q Getter) (string, error) {
	var url string
	if err := q.GetContext(ctx, &url, "SELECT value FROM settings WHERE name = 'payment_gateway_url'"); err != nil {
		return "", err
	}
	return url, nil
}

// UpdatePaymentGatewayURL は決済サーバのURLを更新する。初期化用。
func (r *SettingsRepository) UpdatePaymentGatewayURL(ctx context.Context, q Queryer, url string) error {
	_, err := q.ExecContext(ctx, "UPDATE settings SET value = ? WHERE name = 'payment_gateway_url'", url)
	return err
}
