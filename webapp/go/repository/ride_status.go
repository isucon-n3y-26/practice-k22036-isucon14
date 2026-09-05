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

func (r *RideStatusRepository) GetLatestStatusByRideID(ctx context.Context, q Getter, rideID string) (string, error) {
	var status string
	if err := q.GetContext(ctx, &status,
		"SELECT status FROM ride_statuses WHERE ride_id = ? ORDER BY created_at DESC LIMIT 1",
		rideID,
	); err != nil {
		return "", err
	}
	return status, nil
}

func (r *RideStatusRepository) ListByRideID(ctx context.Context, q Selecter, rideID string) ([]models.RideStatus, error) {
	statuses := []models.RideStatus{}
	if err := q.SelectContext(ctx, &statuses,
		`SELECT * FROM ride_statuses WHERE ride_id = ? ORDER BY created_at`,
		rideID,
	); err != nil {
		return nil, err
	}
	return statuses, nil
}

// ListUnsentAppByUserID はユーザー向け未送信ステータスを時系列昇順で返す。
// 複数ライドに跨る順序を保証するため status の created_at でソートする。
func (r *RideStatusRepository) ListUnsentAppByUserID(ctx context.Context, q Selecter, userID string, limit int) ([]models.RideStatus, error) {
	statuses := []models.RideStatus{}
	if err := q.SelectContext(ctx, &statuses, `
		SELECT rs.* FROM ride_statuses rs
		INNER JOIN rides r ON r.id = rs.ride_id
		WHERE r.user_id = ? AND rs.app_sent_at IS NULL
		ORDER BY rs.created_at ASC
		LIMIT ?
	`, userID, limit); err != nil {
		return nil, err
	}
	return statuses, nil
}

// ListUnsentChairByChairID は椅子向け未送信ステータスを送信順に返す。
// SSE化前のJSONポーリング（GetRidesWithUnsentStatusByChairID +
// GetOldestUnsentByRideID の繰り返し）と同順序になるよう、
// COMPLETED未送信を含むライドを優先し、ライド内では時系列昇順に返す。
// これにより、前ライドのCOMPLETEDより先に次ライドのMATCHINGが送られることを防ぐ。
func (r *RideStatusRepository) ListUnsentChairByChairID(ctx context.Context, q Selecter, chairID string, limit int) ([]models.RideStatus, error) {
	statuses := []models.RideStatus{}
	if err := q.SelectContext(ctx, &statuses, `
		WITH unsent AS (
			SELECT rs.ride_id,
			       MIN(CASE rs.status WHEN 'COMPLETED' THEN 0 ELSE 1 END) as priority,
			       MIN(rs.created_at) as oldest_created
			FROM ride_statuses rs
			WHERE rs.chair_sent_at IS NULL
			  AND rs.ride_id IN (SELECT id FROM rides WHERE chair_id = ?)
			GROUP BY rs.ride_id
		)
		SELECT rs.* FROM ride_statuses rs
		INNER JOIN rides r ON r.id = rs.ride_id
		INNER JOIN unsent u ON u.ride_id = rs.ride_id
		WHERE r.chair_id = ? AND rs.chair_sent_at IS NULL
		ORDER BY u.priority, u.oldest_created, rs.created_at ASC
		LIMIT ?
	`, chairID, chairID, limit); err != nil {
		return nil, err
	}
	return statuses, nil
}

func (r *RideStatusRepository) MarkAppSent(ctx context.Context, q Queryer, id string) error {
	_, err := q.ExecContext(
		ctx,
		"UPDATE ride_statuses SET app_sent_at = CURRENT_TIMESTAMP(6) WHERE id = ? AND app_sent_at IS NULL",
		id,
	)
	return err
}

func (r *RideStatusRepository) MarkChairSent(ctx context.Context, q Queryer, id string) error {
	_, err := q.ExecContext(
		ctx,
		"UPDATE ride_statuses SET chair_sent_at = CURRENT_TIMESTAMP(6) WHERE id = ? AND chair_sent_at IS NULL",
		id,
	)
	return err
}
