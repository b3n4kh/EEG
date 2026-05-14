package web

import (
	"context"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ben/eeg-sumsum/internal/auth"
	"github.com/ben/eeg-sumsum/internal/db"
)

func TestParticipantCannotAccessAdmin(t *testing.T) {
	database := testDB(t)
	hash, err := auth.HashPassword("secret123")
	if err != nil {
		t.Fatal(err)
	}
	if err := database.CreateUser(context.Background(), "teilnehmer", "Teilnehmer", hash, db.RoleParticipant, true); err != nil {
		t.Fatal(err)
	}
	app := New(database, true)
	client, baseURL := testClient(app.Routes())
	login(t, client, baseURL, "teilnehmer", "secret123")

	resp, err := client.Get(baseURL + "/admin")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", resp.StatusCode)
	}
}

func TestParticipantCannotAccessUnassignedMeter(t *testing.T) {
	database := testDB(t)
	hash, err := auth.HashPassword("secret123")
	if err != nil {
		t.Fatal(err)
	}
	if err := database.CreateUser(context.Background(), "teilnehmer", "Teilnehmer", hash, db.RoleParticipant, true); err != nil {
		t.Fatal(err)
	}
	if err := database.UpsertMeteringPoint(context.Background(), nil, db.MeteringPoint{ID: "AT001", Direction: "CONSUMPTION"}); err != nil {
		t.Fatal(err)
	}
	app := New(database, true)
	client, baseURL := testClient(app.Routes())
	login(t, client, baseURL, "teilnehmer", "secret123")

	resp, err := client.Get(baseURL + "/meters/AT001")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
}

func TestUploadAPIRejectsInvalidToken(t *testing.T) {
	database := testDB(t)
	app := New(database, true)
	req := httptest.NewRequest(http.MethodPost, "/api/admin/imports", nil)
	req.Header.Set("Authorization", "Bearer wrong")
	rec := httptest.NewRecorder()
	app.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

func testDB(t *testing.T) *db.DB {
	t.Helper()
	database, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	return database
}

func testClient(handler http.Handler) (*http.Client, string) {
	server := httptest.NewServer(handler)
	client := server.Client()
	jar, _ := cookiejar.New(nil)
	client.Jar = jar
	return client, server.URL
}

func login(t *testing.T, client *http.Client, baseURL, username, password string) {
	t.Helper()
	form := url.Values{"username": {username}, "password": {password}}
	resp, err := client.Post(baseURL+"/login", "application/x-www-form-urlencoded", strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("login final status = %d, want 200", resp.StatusCode)
	}
}
