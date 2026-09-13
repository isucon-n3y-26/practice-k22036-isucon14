package repository

import (
	"context"

	"github.com/jmoiron/sqlx"

	"github.com/isucon/isucon14/webapp/go/models"
)

type PaymentTokenRepository struct {
	db *sqlx.DB
}

func NewPaymentTokenRepository(db *sqlx.DB) *PaymentTokenRepository {
	return &PaymentTokenRepository{db: db}
}

// GetByUserID はユーザーの決済トークンを返す。未登録時は sql.ErrNoRows。
func (r *PaymentTokenRepository) GetByUserID(ctx context.Context, q Getter, userID string) (*models.PaymentToken, error) {
	token := &models.PaymentToken{}
	if err := q.GetContext(ctx, token,
		`SELECT * FROM payment_tokens WHERE user_id = ?`,
		userID,
	); err != nil {
		return nil, err
	}
	return token, nil
}
