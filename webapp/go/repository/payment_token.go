package repository

import (
	"context"
	"sync"

	"github.com/jmoiron/sqlx"

	"github.com/isucon/isucon14/webapp/go/models"
)

type PaymentTokenRepository struct {
	db *sqlx.DB
	// cache は userID -> token の read-through。
	// トークンは登録時のみ書込まれ以後不変（UPDATE文なし）のため等価。
	cache sync.Map
}

func NewPaymentTokenRepository(db *sqlx.DB) *PaymentTokenRepository {
	return &PaymentTokenRepository{db: db}
}

// Create は決済トークンを登録する。ユーザー毎に1行の想定。
func (r *PaymentTokenRepository) Create(ctx context.Context, q Queryer, userID, token string) error {
	_, err := q.ExecContext(
		ctx,
		`INSERT INTO payment_tokens (user_id, token) VALUES (?, ?)`,
		userID,
		token,
	)
	if err != nil {
		return err
	}
	r.cache.Store(userID, token)
	return nil
}

// GetByUserID はユーザーの決済トークンを返す。未登録時は sql.ErrNoRows。
func (r *PaymentTokenRepository) GetByUserID(ctx context.Context, q Getter, userID string) (*models.PaymentToken, error) {
	if v, ok := r.cache.Load(userID); ok {
		return &models.PaymentToken{UserID: userID, Token: v.(string)}, nil
	}
	token := &models.PaymentToken{}
	if err := q.GetContext(ctx, token,
		`SELECT * FROM payment_tokens WHERE user_id = ?`,
		userID,
	); err != nil {
		return nil, err
	}
	r.cache.Store(userID, token.Token)
	return token, nil
}

// ClearCache はキャッシュを破棄する。初期化でDBが全消去されるため。
func (r *PaymentTokenRepository) ClearCache() {
	r.cache = sync.Map{}
}
