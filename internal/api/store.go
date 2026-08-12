package api

import (
	"context"

	"github.com/google/uuid"

	"github.com/komiljonov/ghostman/internal/db"
)

// Store is the slice of the sqlc-generated query API that the HTTP layer uses.
// Declaring it here (rather than passing *db.Queries around) keeps handler
// tests runnable without a database.
type Store interface {
	CreateUser(ctx context.Context, arg db.CreateUserParams) (db.User, error)
	GetUserByEmail(ctx context.Context, email string) (db.User, error)
	GetUserByID(ctx context.Context, id uuid.UUID) (db.User, error)

	CreateSession(ctx context.Context, arg db.CreateSessionParams) (db.Session, error)
	GetSessionByTokenHash(ctx context.Context, tokenHash []byte) (db.GetSessionByTokenHashRow, error)
	DeleteSession(ctx context.Context, tokenHash []byte) error
}

// The generated queries must satisfy Store.
var _ Store = (*db.Queries)(nil)
