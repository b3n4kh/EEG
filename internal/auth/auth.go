package auth

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"errors"

	"golang.org/x/crypto/bcrypt"

	"github.com/ben/eeg-sumsum/internal/db"
)

var ErrInvalidCredentials = errors.New("invalid credentials")

type Service struct {
	DB *db.DB
}

func HashPassword(password string) (string, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	return string(hash), err
}

func CheckPassword(hash, password string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) == nil
}

func (s Service) Authenticate(ctx context.Context, username, password string) (db.User, error) {
	u, err := s.DB.UserByUsername(ctx, username)
	if errors.Is(err, sql.ErrNoRows) {
		return db.User{}, ErrInvalidCredentials
	}
	if err != nil {
		return db.User{}, err
	}
	if !u.Active || !CheckPassword(u.PasswordHash, password) {
		return db.User{}, ErrInvalidCredentials
	}
	return u, nil
}

func (s Service) BootstrapAdmin(ctx context.Context, username, password string) error {
	if username == "" || password == "" {
		return nil
	}
	hash, err := HashPassword(password)
	if err != nil {
		return err
	}
	_, err = s.DB.UpsertUser(ctx, username, username, hash, db.RoleAdmin, true)
	return err
}

func (s Service) BootstrapAPIToken(ctx context.Context, token string) error {
	if token == "" {
		return nil
	}
	hash, err := HashPassword(token)
	if err != nil {
		return err
	}
	return s.DB.UpsertAPIToken(ctx, "env-admin-token", hash)
}

func (s Service) CheckAPIToken(ctx context.Context, token string) (bool, error) {
	if token == "" {
		return false, nil
	}
	hashes, err := s.DB.ActiveTokenHashes(ctx)
	if err != nil {
		return false, err
	}
	for _, hash := range hashes {
		if bcrypt.CompareHashAndPassword([]byte(hash), []byte(token)) == nil {
			return true, nil
		}
	}
	return false, nil
}

func ConstantTimeBearer(header string) string {
	const prefix = "Bearer "
	if len(header) <= len(prefix) {
		return ""
	}
	if subtle.ConstantTimeCompare([]byte(header[:len(prefix)]), []byte(prefix)) != 1 {
		return ""
	}
	return header[len(prefix):]
}
