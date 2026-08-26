package repository

import (
	"context"

	"github.com/jmoiron/sqlx"

	"github.com/isucon/isucon14/webapp/go/models"
)

type RideRepository struct {
	db *sqlx.DB
}

func NewRideRepository(db *sqlx.DB) *RideRepository {
	return &RideRepository{db: db}
}

func (r *RideRepository) GetLatestByChairID(ctx context.Context, q Getter, chairID string) (*models.Ride, error) {
	ride := &models.Ride{}
	if err := q.GetContext(ctx, ride,
		"SELECT * FROM rides WHERE chair_id = ? ORDER BY updated_at DESC LIMIT 1",
		chairID,
	); err != nil {
		return nil, err
	}
	return ride, nil
}
