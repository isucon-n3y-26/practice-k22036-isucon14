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

func (r *ChairRepository) GetByID(ctx context.Context, q Getter, chairID string) (*models.Chair, error) {
	chair := &models.Chair{}
	if err := q.GetContext(ctx, chair,
		"SELECT * FROM chairs WHERE id = ?",
		chairID,
	); err != nil {
		return nil, err
	}
	return chair, nil
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
		`SELECT chair_id, latitude, longitude
FROM (
  SELECT chair_id, latitude, longitude,
         ROW_NUMBER() OVER (PARTITION BY chair_id ORDER BY created_at DESC) as rn
  FROM chair_locations
) t
WHERE rn = 1`,
	); err != nil {
		return nil, err
	}
	return locations, nil
}

func (r *ChairRepository) GetNearestAvailableChair(ctx context.Context, q Getter, pickupLatitude, pickupLongitude int) (*models.NearestChair, error) {
	matched := &models.NearestChair{}
	if err := q.GetContext(ctx, matched, `
		SELECT c.*, cl.latitude, cl.longitude,
		       (ABS(cl.latitude - ?) + ABS(cl.longitude - ?)) AS distance
		FROM chairs c
		JOIN (
			SELECT chair_id, latitude, longitude
			FROM (
				SELECT chair_id, latitude, longitude,
				       ROW_NUMBER() OVER (PARTITION BY chair_id ORDER BY created_at DESC) as rn
				FROM chair_locations
			) t WHERE rn = 1
		) cl ON c.id = cl.chair_id
		WHERE c.is_active = TRUE
		  AND NOT EXISTS (
		    SELECT 1 FROM rides r2
		    JOIN ride_statuses rs2 ON rs2.ride_id = r2.id
		    WHERE r2.chair_id = c.id
		      AND rs2.status <> 'COMPLETED'
		      AND rs2.created_at = (SELECT MAX(created_at) FROM ride_statuses WHERE ride_id = rs2.ride_id)
		  )
		ORDER BY distance
		LIMIT 1
	`, pickupLatitude, pickupLongitude); err != nil {
		return nil, err
	}
	return matched, nil
}
