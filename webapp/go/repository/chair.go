package repository

import (
	"context"

	"github.com/jmoiron/sqlx"

	"github.com/isucon/isucon14/webapp/go/models"
)

type ChairRepository struct {
	db *sqlx.DB
}

func NewChairRepository(db *sqlx.DB) *ChairRepository {
	return &ChairRepository{db: db}
}

func (r *ChairRepository) GetActiveChairs(ctx context.Context, q Selecter) ([]models.Chair, error) {
	chairs := []models.Chair{}
	if err := q.SelectContext(ctx, &chairs,
		"SELECT * FROM chairs WHERE is_active = TRUE",
	); err != nil {
		return nil, err
	}
	return chairs, nil
}

func (r *ChairRepository) GetChairIDsWithIncompleteRide(ctx context.Context, q Selecter) ([]string, error) {
	ids := []string{}
	if err := q.SelectContext(ctx, &ids,
		`SELECT DISTINCT rides.chair_id
FROM rides
JOIN ride_statuses ON ride_statuses.ride_id = rides.id
	AND ride_statuses.created_at = (SELECT MAX(rs2.created_at) FROM ride_statuses rs2 WHERE rs2.ride_id = rides.id)
WHERE rides.chair_id IS NOT NULL
	AND ride_statuses.status <> 'COMPLETED'`,
	); err != nil {
		return nil, err
	}
	return ids, nil
}

func (r *ChairRepository) GetLatestLocations(ctx context.Context, q Selecter) ([]models.ChairLocation, error) {
	locations := []models.ChairLocation{}
	if err := q.SelectContext(ctx, &locations,
		`SELECT cl.chair_id, cl.latitude, cl.longitude
FROM chair_locations cl
WHERE cl.created_at = (SELECT MAX(cl2.created_at) FROM chair_locations cl2 WHERE cl2.chair_id = cl.chair_id)`,
	); err != nil {
		return nil, err
	}
	return locations, nil
}
