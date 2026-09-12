package repository

import (
	"context"

	"github.com/jmoiron/sqlx"

	"github.com/isucon/isucon14/webapp/go/models"
)

type OwnerRepository struct {
	db *sqlx.DB
}

func NewOwnerRepository(db *sqlx.DB) *OwnerRepository {
	return &OwnerRepository{db: db}
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
