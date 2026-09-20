package repository

import (
	"context"
	"sort"
	"strings"
	"sync"
	"time"

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

// Create は椅子を1行挿入する。ID・トークンは呼出し側で採番する。
func (r *ChairRepository) Create(ctx context.Context, q Queryer, chair *models.Chair) error {
	_, err := q.ExecContext(
		ctx,
		"INSERT INTO chairs (id, owner_id, name, model, is_active, access_token) VALUES (?, ?, ?, ?, ?, ?)",
		chair.ID, chair.OwnerID, chair.Name, chair.Model, chair.IsActive, chair.AccessToken,
	)
	return err
}

// UpdateIsActive は椅子の稼働状態を更新する。
func (r *ChairRepository) UpdateIsActive(ctx context.Context, q Queryer, chairID string, isActive bool) error {
	_, err := q.ExecContext(ctx, "UPDATE chairs SET is_active = ? WHERE id = ?", isActive, chairID)
	return err
}

// ListAll は椅子をすべて返す。ChairManagerの起動時再構築用。
func (r *ChairRepository) ListAll(ctx context.Context, q Selecter) ([]models.Chair, error) {
	chairs := []models.Chair{}
	if err := q.SelectContext(ctx, &chairs, "SELECT * FROM chairs"); err != nil {
		return nil, err
	}
	return chairs, nil
}

// ListByOwnerID はオーナー配下の椅子を返す。売上集計用。
func (r *ChairRepository) ListByOwnerID(ctx context.Context, q Selecter, ownerID string) ([]models.Chair, error) {
	chairs := []models.Chair{}
	if err := q.SelectContext(ctx, &chairs, "SELECT * FROM chairs WHERE owner_id = ?", ownerID); err != nil {
		return nil, err
	}
	return chairs, nil
}

// AddTotalDistance は走行距離を加算する。座標POSTの確定用で、同一TX内で呼ぶこと。
func (r *ChairRepository) AddTotalDistance(ctx context.Context, q Queryer, chairID string, delta int) error {
	_, err := q.ExecContext(
		ctx,
		`INSERT INTO chair_total_distances (chair_id, total_distance) VALUES (?, ?)
			 ON DUPLICATE KEY UPDATE total_distance = total_distance + VALUES(total_distance)`,
		chairID,
		delta,
	)
	return err
}

// ChairDistance は椅子ごとの累積走行距離と最終更新時刻。
type ChairDistance struct {
	ChairID   string    `db:"chair_id"`
	Total     int       `db:"total_distance"`
	UpdatedAt time.Time `db:"updated_at"`
}

// ListAllDistances は全椅子の累積走行距離を返す。DistanceBufferのReload同期用。
func (r *ChairRepository) ListAllDistances(ctx context.Context, q Selecter) ([]ChairDistance, error) {
	dists := []ChairDistance{}
	if err := q.SelectContext(ctx, &dists, "SELECT chair_id, total_distance, updated_at FROM chair_total_distances"); err != nil {
		return nil, err
	}
	return dists, nil
}

// GetTotalDistance は椅子1台の累積走行距離を返す。行が無ければ sql.ErrNoRows。
// ownerGetChairs のメモリ未保持フォールバック用。
func (r *ChairRepository) GetTotalDistance(ctx context.Context, q Getter, chairID string) (ChairDistance, error) {
	d := ChairDistance{}
	if err := q.GetContext(ctx, &d, "SELECT chair_id, total_distance, updated_at FROM chair_total_distances WHERE chair_id = ?", chairID); err != nil {
		return ChairDistance{}, err
	}
	return d, nil
}
// AddTotalDistances は複数椅子の走行距離を1本の multi-row upsert で加算する。
// DistanceBuffer の周期flush用で、autocommitで呼ぶこと。
// UpdatedAt は合算POSTの recordedAt 最大値（呼出し側で切上げ済み）を明示挿入し、
// 逐次upsert時の commit 時刻と同等の意味を保つ。
type DistanceDelta struct {
	ChairID   string
	Delta     int
	UpdatedAt time.Time
}

func (r *ChairRepository) AddTotalDistances(ctx context.Context, q Queryer, deltas []DistanceDelta) error {
	if len(deltas) == 0 {
		return nil
	}
	ids := make([]string, 0, len(deltas))
	byID := make(map[string]DistanceDelta, len(deltas))
	for _, d := range deltas {
		if _, ok := byID[d.ChairID]; !ok {
			ids = append(ids, d.ChairID)
		}
		byID[d.ChairID] = d
	}
	sort.Strings(ids)
	var sb strings.Builder
	sb.WriteString("INSERT INTO chair_total_distances (chair_id, total_distance, updated_at) VALUES ")
	args := make([]any, 0, len(ids)*3)
	for i, id := range ids {
		if i > 0 {
			sb.WriteString(",")
		}
		sb.WriteString("(?,?,?)")
		d := byID[id]
		args = append(args, d.ChairID, d.Delta, d.UpdatedAt)
	}
	sb.WriteString(" ON DUPLICATE KEY UPDATE total_distance = total_distance + VALUES(total_distance), updated_at = VALUES(updated_at)")
	_, err := q.ExecContext(ctx, sb.String(), args...)
	return err
}

// ListWithDistanceByOwnerID はオーナー配下の椅子と累積走行距離を返す。椅子一覧表示用。
func (r *ChairRepository) ListWithDistanceByOwnerID(ctx context.Context, q Selecter, ownerID string) ([]models.ChairWithDistance, error) {
	chairs := []models.ChairWithDistance{}
	if err := q.SelectContext(ctx, &chairs, `SELECT chairs.id,
       chairs.owner_id,
       chairs.name,
       chairs.access_token,
       chairs.model,
       chairs.is_active,
       chairs.created_at,
       chairs.updated_at,
       IFNULL(distance.total_distance, 0) AS total_distance,
       distance.updated_at AS total_distance_updated_at
FROM chairs
       LEFT JOIN chair_total_distances distance ON distance.chair_id = chairs.id
WHERE chairs.owner_id = ?
`, ownerID); err != nil {
		return nil, err
	}
	return chairs, nil
}

// ChairCompletedStats は椅子毎の完了集計1行。完了の定義は
// ARRIVED・CARRYING・COMPLETED をすべて含むライドであること。
type ChairCompletedStats struct {
	ChairID string `db:"chair_id"`
	Count   int64  `db:"c"`
	Sum     int64  `db:"s"`
}

// ListCompletedStats は全椅子の完了集計を一括取得する。
// 通知用統計の起動時復元用。
func (r *ChairRepository) ListCompletedStats(ctx context.Context, q Selecter) ([]ChairCompletedStats, error) {
	rows := []ChairCompletedStats{}
	if err := q.SelectContext(ctx, &rows, `
		SELECT r.chair_id, COUNT(*) AS c, COALESCE(SUM(r.evaluation), 0) AS s
		FROM rides r
		WHERE r.evaluation IS NOT NULL
		  AND EXISTS (SELECT 1 FROM ride_statuses s1 WHERE s1.ride_id = r.id AND s1.status = 'ARRIVED')
		  AND EXISTS (SELECT 1 FROM ride_statuses s2 WHERE s2.ride_id = r.id AND s2.status = 'CARRYING')
		  AND EXISTS (SELECT 1 FROM ride_statuses s3 WHERE s3.ride_id = r.id AND s3.status = 'COMPLETED')
		GROUP BY r.chair_id
	`); err != nil {
		return nil, err
	}
	return rows, nil
}

// GetByID はIDで椅子を1件返す。
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

// GetLatestLocations は椅子毎の最新位置を一括取得する。
// ChairManagerの起動時再構築用。
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
