package repository

import (
	"context"

	"github.com/jmoiron/sqlx"

	"github.com/isucon/isucon14/webapp/go/models"
)

type RideStatusRepository struct {
	db *sqlx.DB
}

func NewRideStatusRepository(db *sqlx.DB) *RideStatusRepository {
	return &RideStatusRepository{db: db}
}

func (r *RideStatusRepository) GetOldestUnsentByRideID(ctx context.Context, q Getter, rideID string) (*models.RideStatus, error) {
	rideStatus := &models.RideStatus{}
	if err := q.GetContext(ctx, rideStatus,
		"SELECT * FROM ride_statuses WHERE ride_id = ? AND chair_sent_at IS NULL ORDER BY created_at ASC LIMIT 1",
		rideID,
	); err != nil {
		return nil, err
	}
	return rideStatus, nil
}
