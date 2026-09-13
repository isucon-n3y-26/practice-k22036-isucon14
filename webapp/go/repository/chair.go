package repository

import (
	"context"
	"sync"

	"github.com/jmoiron/sqlx"

	"github.com/isucon/isucon14/webapp/go/models"
)

type ChairRepository struct {
	db    *sqlx.DB
	cache sync.Map
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

// GetByAccessToken はトークン不変のためキャッシュする。認証ミドルウェアの都度SELECT排除用。
func (r *ChairRepository) GetByAccessToken(ctx context.Context, accessToken string) (*models.Chair, error) {
	if v, ok := r.cache.Load(accessToken); ok {
		return v.(*models.Chair), nil
	}
	chair := &models.Chair{}
	if err := r.db.GetContext(ctx, chair, "SELECT * FROM chairs WHERE access_token = ?", accessToken); err != nil {
		return nil, err
	}
	r.cache.Store(accessToken, chair)
	return chair, nil
}

// ClearCache は認証キャッシュを破棄する。初期化でDBが全消去されるため。
func (r *ChairRepository) ClearCache() {
	r.cache = sync.Map{}
}

// ListByIDs は指定IDの椅子を一括取得する。履歴表示の N+1 解消用。
func (r *ChairRepository) ListByIDs(ctx context.Context, q Selecter, chairIDs []string) ([]models.Chair, error) {
	chairs := []models.Chair{}
	if len(chairIDs) == 0 {
		return chairs, nil
	}
	// IN (?) にはスライスを1引数で渡す（スカラー展開渡しは余剰引数エラーになる）
	query, params, err := sqlx.In(`SELECT * FROM chairs WHERE id IN (?)`, chairIDs)
	if err != nil {
		return nil, err
	}
	if err := q.SelectContext(ctx, &chairs, sqlx.Rebind(sqlx.QUESTION, query), params...); err != nil {
		return nil, err
	}
	return chairs, nil
}

// GetCompletedStats は椅子の完了ライド数と評価平均を1クエリで取得する。
// 完了の定義は getChairStats と同一（ARRIVED・CARRYING・COMPLETED の
// ステータスをすべて含むライドを数える）。
func (r *ChairRepository) GetCompletedStats(ctx context.Context, q Getter, chairID string) (models.ChairStats, error) {
	stats := models.ChairStats{}
	if err := q.GetContext(ctx, &stats, `
		SELECT COUNT(*) AS total_rides_count, COALESCE(AVG(r.evaluation), 0) AS total_evaluation_avg
		FROM rides r
		WHERE r.chair_id = ?
		  AND r.evaluation IS NOT NULL
		  AND EXISTS (SELECT 1 FROM ride_statuses s1 WHERE s1.ride_id = r.id AND s1.status = 'ARRIVED')
		  AND EXISTS (SELECT 1 FROM ride_statuses s2 WHERE s2.ride_id = r.id AND s2.status = 'CARRYING')
		  AND EXISTS (SELECT 1 FROM ride_statuses s3 WHERE s3.ride_id = r.id AND s3.status = 'COMPLETED')
	`, chairID); err != nil {
		return models.ChairStats{}, err
	}
	return stats, nil
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
