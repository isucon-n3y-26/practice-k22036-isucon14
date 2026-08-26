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

func (r *UserRepository) GetByAccessToken(ctx context.Context, accessToken string) (*models.User, error) {
	user := &models.User{}
	if err := r.db.GetContext(ctx, user, "SELECT * FROM users WHERE access_token = ?", accessToken); err != nil {
		return nil, err
	}
	r.cache.Store(user.ID, user)
	return user, nil
}

func (r *UserRepository) GetByInvitationCode(ctx context.Context, invitationCode string) (*models.User, error) {
	user := &models.User{}
	if err := r.db.GetContext(ctx, user, "SELECT * FROM users WHERE invitation_code = ?", invitationCode); err != nil {
		return nil, err
	}
	r.cache.Store(user.ID, user)
	return user, nil
}

func (r *UserRepository) Create(ctx context.Context, q Queryer, user *models.User) error {
	_, err := q.ExecContext(
		ctx,
		"INSERT INTO users (id, username, firstname, lastname, date_of_birth, access_token, invitation_code) VALUES (?, ?, ?, ?, ?, ?, ?)",
		user.ID, user.Username, user.Firstname, user.Lastname, user.DateOfBirth, user.AccessToken, user.InvitationCode,
	)
	return err
}
