package repository

import (
	"context"
	"strings"

	"github.com/jmoiron/sqlx"

	"github.com/isucon/isucon14/webapp/go/models"
)

type ChairLocationRepository struct {
	db *sqlx.DB
}

func NewChairLocationRepository(db *sqlx.DB) *ChairLocationRepository {
	return &ChairLocationRepository{db: db}
}

// BulkCreate は位置行を multi-row INSERT で一括書込みする。
// LocationBufferの周期flush用。
func (r *ChairLocationRepository) BulkCreate(ctx context.Context, q Queryer, locations []models.ChairLocation) error {
	var sb strings.Builder
	sb.WriteString(`INSERT INTO chair_locations (id, chair_id, latitude, longitude, created_at) VALUES `)
	args := make([]any, 0, len(locations)*5)
	for i, e := range locations {
		if i > 0 {
			sb.WriteString(",")
		}
		sb.WriteString("(?,?,?,?,?)")
		args = append(args, e.ID, e.ChairID, e.Latitude, e.Longitude, e.CreatedAt)
	}
	_, err := q.ExecContext(ctx, sb.String(), args...)
	return err
}
