package repository

import (
	"context"
	"database/sql"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/jmoiron/sqlx"

	"github.com/isucon/isucon14/webapp/go/models"
)

type RideStatusRepository struct {
	db *sqlx.DB
}

func NewRideStatusRepository(db *sqlx.DB) *RideStatusRepository {
	return &RideStatusRepository{db: db}
}

// Create は状態遷移を1行挿入する。IDはULIDを想定し呼出し側で採番する。
func (r *RideStatusRepository) Create(ctx context.Context, q Queryer, id, rideID, status string) error {
	_, err := q.ExecContext(ctx,
		`INSERT INTO ride_statuses (id, ride_id, status) VALUES (?, ?, ?)`,
		id, rideID, status,
	)
	return err
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

// MarkAppSentBulk は複数行の app 向け送信済みマーカーを1本のUPDATEで付ける。
// SentMarkBuffer の周期flush用。意味は単発版の束と等価
// （NULL行のみ更新・冪等）。
func (r *RideStatusRepository) MarkAppSentBulk(ctx context.Context, q Queryer, ids []string) error {
	return markSentBulk(ctx, q, "app_sent_at", ids)
}

// UnsentStatusRow は未送信行の backfill 用1行。配送先解決のため rides を結合する。
type UnsentStatusRow struct {
	ID          string         `db:"id"`
	RideID      string         `db:"ride_id"`
	Status      string         `db:"status"`
	CreatedAt   time.Time      `db:"created_at"`
	AppSentAt   *time.Time     `db:"app_sent_at"`
	ChairSentAt *time.Time     `db:"chair_sent_at"`
	UserID      string         `db:"user_id"`
	ChairID     sql.NullString `db:"chair_id"`
}

// ListUnsent は椅子・ユーザーのどちらかが未送信の行を時系列昇順で返す。
// インメモリ未送信ログの起動時・初期化時復元用。
func (r *RideStatusRepository) ListUnsent(ctx context.Context, q Selecter) ([]UnsentStatusRow, error) {
	rows := []UnsentStatusRow{}
	if err := q.SelectContext(ctx, &rows, `
		SELECT rs.id, rs.ride_id, rs.status, rs.created_at,
		       rs.app_sent_at, rs.chair_sent_at, r.user_id, r.chair_id
		FROM ride_statuses rs
		INNER JOIN rides r ON r.id = rs.ride_id
		WHERE rs.chair_sent_at IS NULL OR rs.app_sent_at IS NULL
		ORDER BY rs.created_at ASC
	`); err != nil {
		return nil, err
	}
	return rows, nil
}

func (r *RideStatusRepository) MarkChairSent(ctx context.Context, q Queryer, id string) error {
	_, err := q.ExecContext(
		ctx,
		"UPDATE ride_statuses SET chair_sent_at = CURRENT_TIMESTAMP(6) WHERE id = ? AND chair_sent_at IS NULL",
		id,
	)
	return err
}

// MarkChairSentBulk は複数行の chair 向け送信済みマーカーを1本のUPDATEで付ける。
// SentMarkBuffer の周期flush用。意味は単発版の束と等価
// （NULL行のみ更新・冪等）。
func (r *RideStatusRepository) MarkChairSentBulk(ctx context.Context, q Queryer, ids []string) error {
	return markSentBulk(ctx, q, "chair_sent_at", ids)
}

// markSentBulk は送信済みマーカーの multi-row UPDATE を組み立てる。
// column は内部定数（app_sent_at / chair_sent_at）のみ許す。
func markSentBulk(ctx context.Context, q Queryer, column string, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	if column != "app_sent_at" && column != "chair_sent_at" {
		return errors.New("invalid sent_at column")
	}
	sorted := make([]string, len(ids))
	copy(sorted, ids)
	sort.Strings(sorted)
	var sb strings.Builder
	sb.WriteString("UPDATE ride_statuses SET ")
	sb.WriteString(column)
	sb.WriteString(" = CURRENT_TIMESTAMP(6) WHERE id IN (")
	args := make([]any, 0, len(sorted))
	for i, id := range sorted {
		if i > 0 {
			sb.WriteString(",")
		}
		sb.WriteString("?")
		args = append(args, id)
	}
	sb.WriteString(") AND ")
	sb.WriteString(column)
	sb.WriteString(" IS NULL")
	_, err := q.ExecContext(ctx, sb.String(), args...)
	return err
}
