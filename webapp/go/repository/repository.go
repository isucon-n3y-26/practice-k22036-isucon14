package repository

import (
	"context"
	"database/sql"
)

type Queryer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

type Getter interface {
	GetContext(ctx context.Context, dest any, query string, args ...any) error
}

type Selecter interface {
	SelectContext(ctx context.Context, dest any, query string, args ...any) error
}
