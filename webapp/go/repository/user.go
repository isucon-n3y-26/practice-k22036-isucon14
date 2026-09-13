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
	// トークンは不変のためキャッシュする。認証ミドルウェアの都度SELECT排除用。
	if v, ok := r.cache.Load("token:" + accessToken); ok {
		return v.(*models.User), nil
	}
	user := &models.User{}
	if err := r.db.GetContext(ctx, user, "SELECT * FROM users WHERE access_token = ?", accessToken); err != nil {
		return nil, err
	}
	r.cache.Store(user.ID, user)
	r.cache.Store("token:"+accessToken, user)
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

// ClearCache は認証キャッシュを破棄する。初期化でDBが全消去されるため。
func (r *UserRepository) ClearCache() {
	r.cache = sync.Map{}
}
