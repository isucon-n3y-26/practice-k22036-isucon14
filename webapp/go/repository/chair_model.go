package repository

import (
	"context"

	"github.com/jmoiron/sqlx"

	"github.com/isucon/isucon14/webapp/go/models"
)

type ChairModelRepository struct {
	db *sqlx.DB
}

func NewChairModelRepository(db *sqlx.DB) *ChairModelRepository {
	return &ChairModelRepository{db: db}
}

// ListAll は椅子モデルと速度をすべて返す。ChairManagerの速度解決用。
func (r *ChairModelRepository) ListAll(ctx context.Context, q Selecter) ([]models.ChairModel, error) {
	models := []models.ChairModel{}
	if err := q.SelectContext(ctx, &models, "SELECT name, speed FROM chair_models"); err != nil {
		return nil, err
	}
	return models, nil
}
