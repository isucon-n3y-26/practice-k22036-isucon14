package repository

import (
	"context"
	"sync"

	"github.com/jmoiron/sqlx"

	"github.com/isucon/isucon14/webapp/go/models"
)

type OwnerRepository struct {
	db    *sqlx.DB
	cache sync.Map
}

func NewOwnerRepository(db *sqlx.DB) *OwnerRepository {
	return &OwnerRepository{db: db}
}

// GetByAccessToken はトークン不変のためキャッシュする。認証ミドルウェアの都度SELECT排除用。
func (r *OwnerRepository) GetByAccessToken(ctx context.Context, accessToken string) (*models.Owner, error) {
	if v, ok := r.cache.Load(accessToken); ok {
		return v.(*models.Owner), nil
	}
	owner := &models.Owner{}
	if err := r.db.GetContext(ctx, owner, "SELECT * FROM owners WHERE access_token = ?", accessToken); err != nil {
		return nil, err
	}
	r.cache.Store(accessToken, owner)
	return owner, nil
}

// ListByIDs は指定IDのオーナーを一括取得する。履歴表示の N+1 解消用。
func (r *OwnerRepository) ListByIDs(ctx context.Context, q Selecter, ownerIDs []string) ([]models.Owner, error) {
	owners := []models.Owner{}
	if len(ownerIDs) == 0 {
		return owners, nil
	}
	// IN (?) にはスライスを1引数で渡す（スカラー展開渡しは余剰引数エラーになる）
	query, params, err := sqlx.In(`SELECT * FROM owners WHERE id IN (?)`, ownerIDs)
	if err != nil {
		return nil, err
	}
	if err := q.SelectContext(ctx, &owners, sqlx.Rebind(sqlx.QUESTION, query), params...); err != nil {
		return nil, err
	}
	return owners, nil
}
