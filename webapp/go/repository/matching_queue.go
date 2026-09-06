package repository

import (
	"context"

	"github.com/jmoiron/sqlx"

	"github.com/isucon/isucon14/webapp/go/models"
)

type MatchingQueueRepository struct {
	db *sqlx.DB
}

func NewMatchingQueueRepository(db *sqlx.DB) *MatchingQueueRepository {
	return &MatchingQueueRepository{db: db}
}

// Enqueue はライドをマッチング待ちに登録する。配車作成と同一トランザクションで呼ぶこと。
func (r *MatchingQueueRepository) Enqueue(ctx context.Context, q Queryer, rideID string) error {
	_, err := q.ExecContext(ctx, "INSERT INTO matching_queue (ride_id) VALUES (?)", rideID)
	return err
}

// EnqueueIfMissing は存在しない場合のみ登録する。初期化時のバックフィル用。
func (r *MatchingQueueRepository) EnqueueIfMissing(ctx context.Context, q Queryer, rideID string) error {
	_, err := q.ExecContext(ctx, "INSERT IGNORE INTO matching_queue (ride_id) VALUES (?)", rideID)
	return err
}

// Dequeue は割当確定時にキューから取り除く。椅子割当と同一トランザクションで呼ぶこと。
func (r *MatchingQueueRepository) Dequeue(ctx context.Context, q Queryer, rideID string) error {
	_, err := q.ExecContext(ctx, "DELETE FROM matching_queue WHERE ride_id = ?", rideID)
	return err
}

// ListPendingRides はマッチング待ちライドを古い順に返す。
// キューに積まれたIDのみを見るため、全表JOIN走査が不要。
func (r *MatchingQueueRepository) ListPendingRides(ctx context.Context, q Selecter) ([]models.Ride, error) {
	rides := []models.Ride{}
	if err := q.SelectContext(ctx, &rides, `
		SELECT r.* FROM rides r
		INNER JOIN matching_queue q ON q.ride_id = r.id
		ORDER BY r.created_at
	`); err != nil {
		return nil, err
	}
	return rides, nil
}
