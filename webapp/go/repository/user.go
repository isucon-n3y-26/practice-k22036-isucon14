package repository

import (
	"context"
	"sync"

	"github.com/jmoiron/sqlx"

	"github.com/isucon/isucon14/webapp/go/models"
)

type UserRepository struct {
	db    *sqlx.DB
	cache sync.Map
}

func NewUserRepository(db *sqlx.DB) *UserRepository {
	return &UserRepository{db: db}
}

func (r *UserRepository) GetByID(ctx context.Context, id string) (*models.User, error) {
	if v, ok := r.cache.Load(id); ok {
		return v.(*models.User), nil
	}
	user := &models.User{}
	if err := r.db.GetContext(ctx, user, "SELECT * FROM users WHERE id = ?", id); err != nil {
		return nil, err
	}
	r.cache.Store(id, user)
	return user, nil
}
