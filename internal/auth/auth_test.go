package auth

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ben/eeg-sumsum/internal/db"
)

func TestHashAndCheckPassword(t *testing.T) {
	hash, err := HashPassword("secret12345")
	require.NoError(t, err)
	require.NotEqual(t, "secret12345", hash)
	require.True(t, CheckPassword(hash, "secret12345"))
	require.False(t, CheckPassword(hash, "wrong"))
	require.False(t, CheckPassword("not-a-bcrypt-hash", "secret12345"))
}

func TestAuthenticateRejectsInvalidAndInactiveUsers(t *testing.T) {
	database := authTestDB(t)
	service := Service{DB: database}
	hash, err := HashPassword("secret12345")
	require.NoError(t, err)
	require.NoError(t, database.CreateUser(context.Background(), "active", "Active", hash, db.RoleParticipant, true))
	require.NoError(t, database.CreateUser(context.Background(), "inactive", "Inactive", hash, db.RoleParticipant, false))

	user, err := service.Authenticate(context.Background(), "active", "secret12345")
	require.NoError(t, err)
	require.Equal(t, "active", user.Username)

	_, err = service.Authenticate(context.Background(), "active", "wrong")
	require.ErrorIs(t, err, ErrInvalidCredentials)
	_, err = service.Authenticate(context.Background(), "inactive", "secret12345")
	require.ErrorIs(t, err, ErrInvalidCredentials)
	_, err = service.Authenticate(context.Background(), "missing", "secret12345")
	require.ErrorIs(t, err, ErrInvalidCredentials)
}

func TestBootstrapAdminAndToken(t *testing.T) {
	database := authTestDB(t)
	service := Service{DB: database}

	require.NoError(t, service.BootstrapAdmin(context.Background(), "admin", "admin12345"))
	admin, err := service.Authenticate(context.Background(), "admin", "admin12345")
	require.NoError(t, err)
	require.True(t, admin.IsAdmin())

	require.NoError(t, service.BootstrapAPIToken(context.Background(), "dev-token"))
	ok, err := service.CheckAPIToken(context.Background(), "dev-token")
	require.NoError(t, err)
	require.True(t, ok)
	ok, err = service.CheckAPIToken(context.Background(), "wrong-token")
	require.NoError(t, err)
	require.False(t, ok)
	ok, err = service.CheckAPIToken(context.Background(), "")
	require.NoError(t, err)
	require.False(t, ok)
}

func TestBootstrapSkipsEmptyValues(t *testing.T) {
	database := authTestDB(t)
	service := Service{DB: database}
	require.NoError(t, service.BootstrapAdmin(context.Background(), "", ""))
	require.NoError(t, service.BootstrapAPIToken(context.Background(), ""))
	_, err := database.UserByUsername(context.Background(), "")
	require.True(t, errors.Is(err, sql.ErrNoRows))
}

func TestConstantTimeBearer(t *testing.T) {
	require.Equal(t, "token", ConstantTimeBearer("Bearer token"))
	require.Equal(t, "", ConstantTimeBearer("bearer token"))
	require.Equal(t, "", ConstantTimeBearer("Bearer"))
	require.Equal(t, "", ConstantTimeBearer(""))
}

func authTestDB(t *testing.T) *db.DB {
	t.Helper()
	database, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = database.Close() })
	return database
}
