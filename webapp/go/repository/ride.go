package repository

import (
	"context"
	"time"

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

func (r *RideRepository) GetByID(ctx context.Context, q Getter, rideID string) (*models.Ride, error) {
	ride := &models.Ride{}
	if err := q.GetContext(ctx, ride,
		"SELECT * FROM rides WHERE id = ?",
		rideID,
	); err != nil {
		return nil, err
	}
	return ride, nil
}

// GetAssignmentByID は割当確認用に id/user_id/chair_id のみを取得する。
// 存在・割当確認のみが目的のため FOR UPDATE は付けない。
func (r *RideRepository) GetAssignmentByID(ctx context.Context, q Getter, rideID string) (*models.Ride, error) {
	ride := &models.Ride{}
	if err := q.GetContext(ctx, ride,
		"SELECT id, user_id, chair_id FROM rides WHERE id = ?",
		rideID,
	); err != nil {
		return nil, err
	}
	return ride, nil
}

func (r *RideRepository) GetLatestByUserID(ctx context.Context, q Getter, userID string) (*models.Ride, error) {
	ride := &models.Ride{}
	if err := q.GetContext(ctx, ride,
		"SELECT * FROM rides WHERE user_id = ? ORDER BY created_at DESC LIMIT 1",
		userID,
	); err != nil {
		return nil, err
	}
	return ride, nil
}

// ListCompletedByUserID は最新状態が COMPLETED のライドのみを作成降順で返す。
func (r *RideRepository) ListCompletedByUserID(ctx context.Context, q Selecter, userID string) ([]models.Ride, error) {
	rides := []models.Ride{}
	if err := q.SelectContext(ctx, &rides, `
		SELECT r.* FROM rides r
		WHERE r.user_id = ?
		  AND (SELECT rs.status FROM ride_statuses rs
		       WHERE rs.ride_id = r.id ORDER BY rs.created_at DESC LIMIT 1) = 'COMPLETED'
		ORDER BY r.created_at DESC
	`, userID); err != nil {
		return nil, err
	}
	return rides, nil
}

// ListCompletedByOwnerID はオーナー配下の椅子の完了ライドをすべて返す。
// 売上集計用に、椅子毎の取得 N+1 を1クエリにまとめたもの。
func (r *RideRepository) ListCompletedByOwnerID(ctx context.Context, q Selecter, ownerID string, since, until time.Time) ([]models.Ride, error) {
	rides := []models.Ride{}
	if err := q.SelectContext(ctx, &rides, `
		SELECT r.* FROM rides r
		INNER JOIN chairs c ON c.id = r.chair_id AND c.owner_id = ?
		INNER JOIN ride_statuses rs ON rs.ride_id = r.id
		WHERE rs.status = 'COMPLETED'
		  AND r.updated_at BETWEEN ? AND ? + INTERVAL 999 MICROSECOND
	`, ownerID, since, until); err != nil {
		return nil, err
	}
	return rides, nil
}

// CountContinuingByUserID は最新状態が COMPLETED でないライド数を返す。
func (r *RideRepository) CountContinuingByUserID(ctx context.Context, q Getter, userID string) (int, error) {
	var count int
	if err := q.GetContext(ctx, &count, `
		SELECT COUNT(*) FROM rides r
		WHERE r.user_id = ?
		  AND (SELECT rs.status FROM ride_statuses rs
		       WHERE rs.ride_id = r.id ORDER BY rs.created_at DESC LIMIT 1) != 'COMPLETED'
	`, userID); err != nil {
		return 0, err
	}
	return count, nil
}

func (r *RideRepository) ListByChairID(ctx context.Context, q Selecter, chairID string) ([]models.Ride, error) {
	rides := []models.Ride{}
	if err := q.SelectContext(ctx, &rides,
		`SELECT * FROM rides WHERE chair_id = ? ORDER BY updated_at DESC`,
		chairID,
	); err != nil {
		return nil, err
	}
	return rides, nil
}

func (r *RideRepository) GetUnassignedMatchingRides(ctx context.Context, q Selecter) ([]models.Ride, error) {
	rides := []models.Ride{}
	// 最新状態が MATCHING の未割当ライドを古い順に返す。
	// 「MATCHING 行が存在し、それより新しい行が存在しない」を
	// anti-join で表すことで、相関MAXの導出テーブルをなくしている。
	if err := q.SelectContext(ctx, &rides, `
		SELECT r.* FROM rides r
		INNER JOIN ride_statuses m ON m.ride_id = r.id AND m.status = 'MATCHING'
		LEFT JOIN ride_statuses later ON later.ride_id = r.id AND later.created_at > m.created_at
		WHERE r.chair_id IS NULL
		  AND later.id IS NULL
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

// UpdateEvaluation は評価値を更新し、更新行数を返す。0件の場合は不存在扱い。
func (r *RideRepository) UpdateEvaluation(ctx context.Context, q Queryer, rideID string, evaluation int) (int64, error) {
	result, err := q.ExecContext(ctx, "UPDATE rides SET evaluation = ? WHERE id = ?", evaluation, rideID)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

// ListByUserID はユーザーのライドを作成昇順で返す。
// 決済リトライ時の件数照合用で、評価の書込み前後で内容は不変のためTX外で読める。
func (r *RideRepository) ListByUserID(ctx context.Context, q Selecter, userID string) ([]models.Ride, error) {
	rides := []models.Ride{}
	if err := q.SelectContext(ctx, &rides,
		`SELECT * FROM rides WHERE user_id = ? ORDER BY created_at ASC`,
		userID,
	); err != nil {
		return nil, err
	}
	return rides, nil
}
