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

func (r *RideRepository) GetUnassignedMatchingRides(ctx context.Context, q Selecter) ([]models.Ride, error) {
	rides := []models.Ride{}
	if err := q.SelectContext(ctx, &rides, `
		SELECT r.* FROM rides r
		JOIN (
			SELECT ride_id, status FROM ride_statuses rs
			WHERE rs.created_at = (SELECT MAX(created_at) FROM ride_statuses WHERE ride_id = rs.ride_id)
		) latest_rs ON latest_rs.ride_id = r.id
		WHERE r.chair_id IS NULL
		  AND latest_rs.status = 'MATCHING'
		ORDER BY r.created_at
	`); err != nil {
		return nil, err
	}
	return rides, nil
}

func (r *RideRepository) UpdateChairID(ctx context.Context, q Queryer, rideID, chairID string) error {
	_, err := q.ExecContext(ctx, "UPDATE rides SET chair_id = ? WHERE id = ?", chairID, rideID)
	return err
}
