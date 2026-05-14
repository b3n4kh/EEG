package web

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/a-h/templ"
	"github.com/alexedwards/scs/v2"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/ben/eeg-sumsum/internal/auth"
	"github.com/ben/eeg-sumsum/internal/charts"
	"github.com/ben/eeg-sumsum/internal/db"
	"github.com/ben/eeg-sumsum/internal/eda"
	"github.com/ben/eeg-sumsum/internal/imports"
	"github.com/ben/eeg-sumsum/internal/views"
)

type Server struct {
	DB       *db.DB
	Auth     auth.Service
	Importer imports.Importer
	EDA      eda.Client
	Sessions *scs.SessionManager
}

func New(database *db.DB, devMode bool, edaConfigs ...eda.Config) *Server {
	sessionManager := scs.New()
	sessionManager.Lifetime = 24 * time.Hour
	sessionManager.Cookie.HttpOnly = true
	sessionManager.Cookie.Persist = true
	sessionManager.Cookie.SameSite = http.SameSiteLaxMode
	sessionManager.Cookie.Secure = !devMode
	var edaConfig eda.Config
	if len(edaConfigs) > 0 {
		edaConfig = edaConfigs[0]
	}
	s := &Server{
		DB:       database,
		Auth:     auth.Service{DB: database},
		Importer: imports.Importer{DB: database},
		EDA:      eda.Client{Config: edaConfig},
		Sessions: sessionManager,
	}
	return s
}

func (s *Server) Routes() http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)
	r.Use(s.Sessions.LoadAndSave)
	r.Get("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	r.Get("/login", s.loginForm)
	r.Post("/login", s.login)
	r.Post("/logout", s.logout)
	r.Post("/api/admin/imports", s.apiImport)
	r.Post("/api/admin/eda-imports", s.apiEDAImport)
	r.Group(func(r chi.Router) {
		r.Use(s.requireLogin)
		r.Post("/impersonation/stop", s.stopImpersonating)
		r.Get("/password/change", s.passwordChangeForm)
		r.Post("/password/change", s.passwordChange)
	})
	r.Group(func(r chi.Router) {
		r.Use(s.requireLogin)
		r.Use(s.requirePasswordCurrent)
		r.Get("/", s.dashboard)
		r.Get("/meters/{id}", s.meter)
	})
	r.Group(func(r chi.Router) {
		r.Use(s.requireLogin)
		r.Use(s.requirePasswordCurrent)
		r.Use(s.requireAdmin)
		r.Get("/admin", s.admin)
		r.Get("/admin/users/new", s.newUserForm)
		r.Post("/admin/users", s.createUser)
		r.Get("/admin/users/{id}", s.editUserForm)
		r.Post("/admin/users/{id}", s.updateUser)
		r.Post("/admin/users/{id}/impersonate", s.impersonateUser)
		r.Post("/admin/imports", s.adminImport)
		r.Post("/admin/eda-imports", s.adminEDAImport)
	})
	r.NotFound(func(w http.ResponseWriter, r *http.Request) {
		s.render(w, r, http.StatusNotFound, views.NotFound(s.currentUser(r.Context())))
	})
	return r
}

func (s *Server) loginForm(w http.ResponseWriter, r *http.Request) {
	if s.sessionUserID(r) != 0 {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	s.render(w, r, http.StatusOK, views.Login(""))
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.render(w, r, http.StatusBadRequest, views.Login("Ungültiges Formular."))
		return
	}
	user, err := s.Auth.Authenticate(r.Context(), r.FormValue("username"), r.FormValue("password"))
	if err != nil {
		s.render(w, r, http.StatusUnauthorized, views.Login("Benutzername oder Passwort ist falsch."))
		return
	}
	if err := s.Sessions.RenewToken(r.Context()); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.Sessions.Put(r.Context(), "user_id", user.ID)
	if user.PasswordChangeRequired {
		http.Redirect(w, r, "/password/change", http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	_ = s.Sessions.Destroy(r.Context())
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

func (s *Server) dashboard(w http.ResponseWriter, r *http.Request) {
	user := s.currentUser(r.Context())
	var participantSummaries []db.ParticipantMeterSummary
	if !user.IsAdmin() {
		summaries, err := s.DB.ParticipantMeterSummaries(r.Context(), user.ID)
		if err != nil {
			s.serverError(w, r, err)
			return
		}
		participantSummaries = summaries
	}
	meters, err := s.DB.MeteringPoints(r.Context(), &user)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.render(w, r, http.StatusOK, views.Dashboard(user, meters, participantSummaries, s.takeFlash(r.Context())))
}

func (s *Server) meter(w http.ResponseWriter, r *http.Request) {
	user := s.currentUser(r.Context())
	id := chi.URLParam(r, "id")
	meter, err := s.DB.Meter(r.Context(), id, &user)
	if errors.Is(err, sql.ErrNoRows) {
		s.render(w, r, http.StatusNotFound, views.NotFound(user))
		return
	}
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	metrics, err := s.DB.MetricLabels(r.Context(), id)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	selected := r.URL.Query().Get("metric")
	if selected == "" && len(metrics) > 0 {
		selected = metrics[0].Key
	}
	points, err := s.DB.Series(r.Context(), id, selected, 384)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	label := selected
	for _, metric := range metrics {
		if metric.Key == selected {
			label = metric.Label
			break
		}
	}
	s.render(w, r, http.StatusOK, views.Meter(user, meter, metrics, selected, charts.LineSVG(points, label), points))
}

func (s *Server) passwordChangeForm(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, http.StatusOK, views.PasswordChange(s.currentUser(r.Context()), ""))
}

func (s *Server) passwordChange(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.serverError(w, r, err)
		return
	}
	user := s.currentUser(r.Context())
	current := r.FormValue("current_password")
	password := r.FormValue("password")
	confirm := r.FormValue("password_confirm")
	switch {
	case !auth.CheckPassword(user.PasswordHash, current):
		s.render(w, r, http.StatusUnauthorized, views.PasswordChange(user, "Das aktuelle Passwort ist falsch."))
		return
	case len(password) < 10:
		s.render(w, r, http.StatusBadRequest, views.PasswordChange(user, "Das neue Passwort muss mindestens 10 Zeichen lang sein."))
		return
	case password != confirm:
		s.render(w, r, http.StatusBadRequest, views.PasswordChange(user, "Die neuen Passwörter stimmen nicht überein."))
		return
	}
	hash, err := auth.HashPassword(password)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	if err := s.DB.UpdatePassword(r.Context(), user.ID, hash, false); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.putFlash(r.Context(), views.Flash{Message: "Passwort wurde geändert."})
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) admin(w http.ResponseWriter, r *http.Request) {
	user := s.currentUser(r.Context())
	meters, err := s.DB.MeteringPoints(r.Context(), nil)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	users, err := s.DB.Users(r.Context())
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.render(w, r, http.StatusOK, views.Admin(user, meters, users, s.takeFlash(r.Context()), s.EDA.Config.Enabled()))
}

func (s *Server) newUserForm(w http.ResponseWriter, r *http.Request) {
	s.userForm(w, r, views.UserForm{NewUser: true, User: db.User{Role: db.RoleParticipant, Active: true}})
}

func (s *Server) editUserForm(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		s.render(w, r, http.StatusNotFound, views.NotFound(s.currentUser(r.Context())))
		return
	}
	user, err := s.DB.UserByID(r.Context(), id)
	if errors.Is(err, sql.ErrNoRows) {
		s.render(w, r, http.StatusNotFound, views.NotFound(s.currentUser(r.Context())))
		return
	}
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	assigned, err := s.DB.AssignedMeterIDs(r.Context(), id)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.userForm(w, r, views.UserForm{User: user, Assigned: assigned})
}

func (s *Server) userForm(w http.ResponseWriter, r *http.Request, form views.UserForm) {
	user := s.currentUser(r.Context())
	meters, err := s.DB.MeteringPoints(r.Context(), nil)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	if form.Assigned == nil {
		form.Assigned = map[string]bool{}
	}
	s.render(w, r, http.StatusOK, views.UserEdit(user, form, meters))
}

func (s *Server) createUser(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.serverError(w, r, err)
		return
	}
	password := r.FormValue("password")
	form := views.UserForm{
		NewUser: true,
		User: db.User{
			Username:    r.FormValue("username"),
			DisplayName: r.FormValue("display_name"),
			Role:        roleValue(r.FormValue("role")),
			Active:      r.FormValue("active") == "1",
		},
		Assigned: checkedMeters(r),
	}
	if form.User.Username == "" || form.User.DisplayName == "" || password == "" {
		form.Error = "Login, Anzeigename und Passwort sind Pflichtfelder."
		s.userForm(w, r, form)
		return
	}
	hash, err := auth.HashPassword(password)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	if err := s.DB.CreateUser(r.Context(), form.User.Username, form.User.DisplayName, hash, form.User.Role, form.User.Active); err != nil {
		form.Error = "Benutzer konnte nicht angelegt werden: " + err.Error()
		s.userForm(w, r, form)
		return
	}
	created, err := s.DB.UserByUsername(r.Context(), form.User.Username)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	if err := s.DB.AssignMeters(r.Context(), created.ID, r.Form["meters"]); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.putFlash(r.Context(), views.Flash{Message: "Benutzer wurde angelegt."})
	http.Redirect(w, r, "/admin", http.StatusSeeOther)
}

func (s *Server) updateUser(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		s.render(w, r, http.StatusNotFound, views.NotFound(s.currentUser(r.Context())))
		return
	}
	if err := r.ParseForm(); err != nil {
		s.serverError(w, r, err)
		return
	}
	user, err := s.DB.UserByID(r.Context(), id)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	form := views.UserForm{
		User: db.User{
			ID:          id,
			Username:    user.Username,
			DisplayName: r.FormValue("display_name"),
			Role:        roleValue(r.FormValue("role")),
			Active:      r.FormValue("active") == "1",
		},
		Assigned: checkedMeters(r),
	}
	if form.User.DisplayName == "" {
		form.Error = "Anzeigename ist ein Pflichtfeld."
		s.userForm(w, r, form)
		return
	}
	if err := s.DB.UpdateUser(r.Context(), id, form.User.DisplayName, form.User.Role, form.User.Active); err != nil {
		s.serverError(w, r, err)
		return
	}
	if password := r.FormValue("password"); password != "" {
		hash, err := auth.HashPassword(password)
		if err != nil {
			s.serverError(w, r, err)
			return
		}
		if err := s.DB.UpdatePassword(r.Context(), id, hash, form.User.Role == db.RoleParticipant); err != nil {
			s.serverError(w, r, err)
			return
		}
	}
	if err := s.DB.AssignMeters(r.Context(), id, r.Form["meters"]); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.putFlash(r.Context(), views.Flash{Message: "Benutzer wurde gespeichert."})
	http.Redirect(w, r, "/admin", http.StatusSeeOther)
}

func (s *Server) impersonateUser(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		s.render(w, r, http.StatusNotFound, views.NotFound(s.currentUser(r.Context())))
		return
	}
	admin := s.currentUser(r.Context())
	target, err := s.DB.UserByID(r.Context(), id)
	if errors.Is(err, sql.ErrNoRows) {
		s.render(w, r, http.StatusNotFound, views.NotFound(admin))
		return
	}
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	if !target.Active {
		s.putFlash(r.Context(), views.Flash{Kind: "error", Message: "Inaktive Benutzer können nicht übernommen werden."})
		http.Redirect(w, r, "/admin", http.StatusSeeOther)
		return
	}
	if err := s.Sessions.RenewToken(r.Context()); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.Sessions.Put(r.Context(), "impersonator_user_id", admin.ID)
	s.Sessions.Put(r.Context(), "user_id", target.ID)
	s.putFlash(r.Context(), views.Flash{Message: "Sie sehen das Portal jetzt als " + target.DisplayName + "."})
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) stopImpersonating(w http.ResponseWriter, r *http.Request) {
	adminID := s.impersonatorUserID(r)
	if adminID == 0 {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	admin, err := s.DB.UserByID(r.Context(), adminID)
	if err != nil || !admin.Active || !admin.IsAdmin() {
		_ = s.Sessions.Destroy(r.Context())
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	if err := s.Sessions.RenewToken(r.Context()); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.Sessions.Remove(r.Context(), "impersonator_user_id")
	s.Sessions.Put(r.Context(), "user_id", admin.ID)
	s.putFlash(r.Context(), views.Flash{Message: "Zurück als " + admin.DisplayName + "."})
	http.Redirect(w, r, "/admin", http.StatusSeeOther)
}

func (s *Server) apiImport(w http.ResponseWriter, r *http.Request) {
	token := auth.ConstantTimeBearer(r.Header.Get("Authorization"))
	ok, err := s.Auth.CheckAPIToken(r.Context(), token)
	if err != nil {
		s.writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !ok {
		s.writeJSONError(w, http.StatusUnauthorized, "invalid api token")
		return
	}
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		s.writeJSONError(w, http.StatusBadRequest, "expected multipart form with file field")
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		s.writeJSONError(w, http.StatusBadRequest, "missing file field")
		return
	}
	defer file.Close()
	summary, err := s.Importer.ImportReader(r.Context(), header.Filename, file, nil)
	if err != nil {
		s.writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, summary)
}

func (s *Server) adminImport(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		s.putFlash(r.Context(), views.Flash{Kind: "error", Message: "Ungültiger XLSX Upload."})
		http.Redirect(w, r, "/admin", http.StatusSeeOther)
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		s.putFlash(r.Context(), views.Flash{Kind: "error", Message: "Bitte eine XLSX Datei auswählen."})
		http.Redirect(w, r, "/admin", http.StatusSeeOther)
		return
	}
	defer file.Close()
	user := s.currentUser(r.Context())
	summary, err := s.Importer.ImportReader(r.Context(), header.Filename, file, &user.ID)
	if err != nil {
		s.putFlash(r.Context(), views.Flash{Kind: "error", Message: "XLSX Import fehlgeschlagen: " + err.Error()})
		http.Redirect(w, r, "/admin", http.StatusSeeOther)
		return
	}
	s.putFlash(r.Context(), views.Flash{Message: fmt.Sprintf("XLSX Import abgeschlossen: %d gelesen, %d neu, %d aktualisiert, %d unverändert.",
		summary.MeasurementsRead, summary.MeasurementsInserted, summary.MeasurementsUpdated, summary.MeasurementsSkipped)})
	http.Redirect(w, r, "/admin", http.StatusSeeOther)
}

func (s *Server) apiEDAImport(w http.ResponseWriter, r *http.Request) {
	token := auth.ConstantTimeBearer(r.Header.Get("Authorization"))
	ok, err := s.Auth.CheckAPIToken(r.Context(), token)
	if err != nil {
		s.writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !ok {
		s.writeJSONError(w, http.StatusUnauthorized, "invalid api token")
		return
	}
	from, to, err := edaRange(r)
	if err != nil {
		s.writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	summary, err := s.importEDA(r.Context(), from, to, nil)
	if err != nil {
		s.writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, summary)
}

func (s *Server) adminEDAImport(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.serverError(w, r, err)
		return
	}
	from, to, err := edaRange(r)
	if err != nil {
		s.putFlash(r.Context(), views.Flash{Kind: "error", Message: err.Error()})
		http.Redirect(w, r, "/admin", http.StatusSeeOther)
		return
	}
	user := s.currentUser(r.Context())
	summary, err := s.importEDA(r.Context(), from, to, &user.ID)
	if err != nil {
		s.putFlash(r.Context(), views.Flash{Kind: "error", Message: "EDA Import fehlgeschlagen: " + err.Error()})
		http.Redirect(w, r, "/admin", http.StatusSeeOther)
		return
	}
	s.putFlash(r.Context(), views.Flash{Message: fmt.Sprintf("EDA Import abgeschlossen: %d gelesen, %d neu, %d aktualisiert, %d unverändert.",
		summary.MeasurementsRead, summary.MeasurementsInserted, summary.MeasurementsUpdated, summary.MeasurementsSkipped)})
	http.Redirect(w, r, "/admin", http.StatusSeeOther)
}

func (s *Server) importEDA(ctx context.Context, from, to time.Time, uploadedBy *int64) (db.ImportSummary, error) {
	if !s.EDA.Config.Enabled() {
		slog.Error("EDA import requested but not configured")
		return db.ImportSummary{}, fmt.Errorf("EDA import is not configured")
	}
	slog.Info("running EDA import", "from", from.Format(time.RFC3339), "to", to.Format(time.RFC3339), "uploaded_by_user_id", uploadedBy)
	parsed, err := s.EDA.Fetch(ctx, from, to)
	if err != nil {
		slog.Error("EDA import fetch failed", "from", from.Format(time.RFC3339), "to", to.Format(time.RFC3339), "error", err)
		return db.ImportSummary{}, err
	}
	summary, err := s.Importer.ImportParsed(ctx, parsed, uploadedBy)
	if err != nil {
		slog.Error("EDA import store failed", "filename", parsed.Filename, "measurements", len(parsed.Measurements), "summaries", len(parsed.Summaries), "error", err)
		return db.ImportSummary{}, err
	}
	if err := s.syncEDAParticipants(ctx, parsed.ParticipantAccounts); err != nil {
		slog.Error("EDA participant sync failed", "filename", parsed.Filename, "participant_accounts", len(parsed.ParticipantAccounts), "error", err)
		return db.ImportSummary{}, err
	}
	slog.Info("EDA import stored",
		"filename", summary.Filename,
		"measurements_read", summary.MeasurementsRead,
		"measurements_inserted", summary.MeasurementsInserted,
		"measurements_updated", summary.MeasurementsUpdated,
		"measurements_skipped", summary.MeasurementsSkipped,
		"summaries_read", summary.SummariesRead,
		"summaries_inserted", summary.SummariesInserted,
		"summaries_updated", summary.SummariesUpdated,
	)
	return summary, nil
}

func (s *Server) syncEDAParticipants(ctx context.Context, accounts []imports.ParticipantAccount) error {
	if len(accounts) == 0 {
		slog.Info("EDA participant sync skipped; no participant accounts in community response")
		return nil
	}
	for _, account := range accounts {
		password, err := randomPassword()
		if err != nil {
			return err
		}
		hash, err := auth.HashPassword(password)
		if err != nil {
			return err
		}
		user, created, err := s.DB.UpsertEDAUser(ctx, account.Key, account.Username, account.DisplayName, hash)
		if err != nil {
			return fmt.Errorf("sync EDA participant %s: %w", account.DisplayName, err)
		}
		if err := s.DB.AssignMeters(ctx, user.ID, account.MeteringPointIDs); err != nil {
			return fmt.Errorf("assign EDA participant meters %s: %w", account.DisplayName, err)
		}
		slog.Info("synced EDA participant account",
			"user_id", user.ID,
			"username", user.Username,
			"display_name", user.DisplayName,
			"created", created,
			"metering_points", len(account.MeteringPointIDs),
		)
	}
	return nil
}

func randomPassword() (string, error) {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw[:]), nil
}

func (s *Server) requireLogin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := s.sessionUserID(r)
		if id == 0 {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		user, err := s.DB.UserByID(r.Context(), id)
		if err != nil || !user.Active {
			if s.restoreImpersonator(w, r) {
				return
			}
			_ = s.Sessions.Destroy(r.Context())
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		ctx := context.WithValue(r.Context(), userKey{}, user)
		if impersonator := s.impersonator(r); impersonator.ID != 0 {
			ctx = context.WithValue(ctx, impersonatorKey{}, impersonator)
		}
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (s *Server) requireAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user := s.currentUser(r.Context())
		if !user.IsAdmin() {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) requirePasswordCurrent(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user := s.currentUser(r.Context())
		if user.PasswordChangeRequired && s.currentImpersonator(r.Context()).ID == 0 {
			http.Redirect(w, r, "/password/change", http.StatusSeeOther)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) sessionUserID(r *http.Request) int64 {
	return s.sessionInt64(r, "user_id")
}

func (s *Server) impersonatorUserID(r *http.Request) int64 {
	return s.sessionInt64(r, "impersonator_user_id")
}

func (s *Server) sessionInt64(r *http.Request, key string) int64 {
	v := s.Sessions.Get(r.Context(), key)
	switch id := v.(type) {
	case int64:
		return id
	case int:
		return int64(id)
	default:
		return 0
	}
}

type userKey struct{}
type impersonatorKey struct{}

func (s *Server) currentUser(ctx context.Context) db.User {
	user, _ := ctx.Value(userKey{}).(db.User)
	return user
}

func (s *Server) currentImpersonator(ctx context.Context) db.User {
	user, _ := ctx.Value(impersonatorKey{}).(db.User)
	return user
}

func (s *Server) impersonator(r *http.Request) db.User {
	adminID := s.impersonatorUserID(r)
	if adminID == 0 {
		return db.User{}
	}
	admin, err := s.DB.UserByID(r.Context(), adminID)
	if err != nil || !admin.Active || !admin.IsAdmin() {
		return db.User{}
	}
	return admin
}

func (s *Server) restoreImpersonator(w http.ResponseWriter, r *http.Request) bool {
	admin := s.impersonator(r)
	if admin.ID == 0 {
		return false
	}
	s.Sessions.Remove(r.Context(), "impersonator_user_id")
	s.Sessions.Put(r.Context(), "user_id", admin.ID)
	s.putFlash(r.Context(), views.Flash{Kind: "error", Message: "Übernahme wurde beendet, weil der Zielbenutzer nicht mehr aktiv ist."})
	http.Redirect(w, r, "/admin", http.StatusSeeOther)
	return true
}

func (s *Server) render(w http.ResponseWriter, r *http.Request, status int, component templ.Component) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	ctx := r.Context()
	if impersonator := s.currentImpersonator(ctx); impersonator.ID != 0 {
		ctx = views.WithImpersonator(ctx, impersonator)
	}
	if err := component.Render(ctx, w); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (s *Server) serverError(w http.ResponseWriter, r *http.Request, err error) {
	user := s.currentUser(r.Context())
	s.render(w, r, http.StatusInternalServerError, views.ErrorPage(user, err.Error()))
}

func (s *Server) putFlash(ctx context.Context, f views.Flash) {
	s.Sessions.Put(ctx, "flash_kind", f.Kind)
	s.Sessions.Put(ctx, "flash_message", f.Message)
}

func (s *Server) takeFlash(ctx context.Context) views.Flash {
	f := views.Flash{
		Kind:    fmt.Sprint(s.Sessions.Pop(ctx, "flash_kind")),
		Message: fmt.Sprint(s.Sessions.Pop(ctx, "flash_message")),
	}
	if f.Kind == "<nil>" {
		f.Kind = ""
	}
	if f.Message == "<nil>" {
		f.Message = ""
	}
	return f
}

func roleValue(role string) string {
	if role == db.RoleAdmin {
		return db.RoleAdmin
	}
	return db.RoleParticipant
}

func checkedMeters(r *http.Request) map[string]bool {
	out := map[string]bool{}
	for _, id := range r.Form["meters"] {
		out[id] = true
	}
	return out
}

func edaRange(r *http.Request) (time.Time, time.Time, error) {
	loc, err := time.LoadLocation("Europe/Vienna")
	if err != nil {
		loc = time.Local
	}
	yesterday := time.Now().In(loc).AddDate(0, 0, -1)
	fromValue := strings.TrimSpace(r.FormValue("from"))
	toValue := strings.TrimSpace(r.FormValue("to"))
	if fromValue == "" {
		fromValue = strings.TrimSpace(r.URL.Query().Get("from"))
	}
	if toValue == "" {
		toValue = strings.TrimSpace(r.URL.Query().Get("to"))
	}
	if strings.Contains(r.Header.Get("Content-Type"), "application/json") {
		var body struct {
			From string `json:"from"`
			To   string `json:"to"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil && !errors.Is(err, io.EOF) {
			return time.Time{}, time.Time{}, fmt.Errorf("invalid JSON body")
		}
		if body.From != "" {
			fromValue = strings.TrimSpace(body.From)
		}
		if body.To != "" {
			toValue = strings.TrimSpace(body.To)
		}
	}
	if fromValue == "" {
		fromValue = yesterday.Format("2006-01-02")
	}
	if toValue == "" {
		toValue = yesterday.Format("2006-01-02")
	}
	from, err := parseEDARangeTime(fromValue, true, loc)
	if err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("invalid from value %q", fromValue)
	}
	to, err := parseEDARangeTime(toValue, false, loc)
	if err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("invalid to value %q", toValue)
	}
	if to.Before(from) {
		return time.Time{}, time.Time{}, fmt.Errorf("to must be after from")
	}
	return from, to, nil
}

func parseEDARangeTime(value string, start bool, loc *time.Location) (time.Time, error) {
	value = strings.TrimSpace(value)
	if len(value) == len("2006-01-02") {
		t, err := time.ParseInLocation("2006-01-02", value, loc)
		if err != nil {
			return time.Time{}, err
		}
		if start {
			return t, nil
		}
		return t.Add(23*time.Hour + 45*time.Minute), nil
	}
	for _, layout := range []string{"2006-01-02T15:04", "2006-01-02T15:04:05", time.RFC3339} {
		if t, err := time.ParseInLocation(layout, value, loc); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("unsupported time format")
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func (s *Server) writeJSONError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}
