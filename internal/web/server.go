package web

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/a-h/templ"
	"github.com/alexedwards/scs/v2"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/ben/eeg-sumsum/internal/auth"
	"github.com/ben/eeg-sumsum/internal/charts"
	"github.com/ben/eeg-sumsum/internal/db"
	"github.com/ben/eeg-sumsum/internal/imports"
	"github.com/ben/eeg-sumsum/internal/views"
)

type Server struct {
	DB       *db.DB
	Auth     auth.Service
	Importer imports.Importer
	Sessions *scs.SessionManager
}

func New(database *db.DB, devMode bool) *Server {
	sessionManager := scs.New()
	sessionManager.Lifetime = 24 * time.Hour
	sessionManager.Cookie.HttpOnly = true
	sessionManager.Cookie.Persist = true
	sessionManager.Cookie.SameSite = http.SameSiteLaxMode
	sessionManager.Cookie.Secure = !devMode
	s := &Server{
		DB:       database,
		Auth:     auth.Service{DB: database},
		Importer: imports.Importer{DB: database},
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
	r.Group(func(r chi.Router) {
		r.Use(s.requireLogin)
		r.Get("/", s.dashboard)
		r.Get("/meters/{id}", s.meter)
	})
	r.Group(func(r chi.Router) {
		r.Use(s.requireLogin)
		r.Use(s.requireAdmin)
		r.Get("/admin", s.admin)
		r.Get("/admin/users/new", s.newUserForm)
		r.Post("/admin/users", s.createUser)
		r.Get("/admin/users/{id}", s.editUserForm)
		r.Post("/admin/users/{id}", s.updateUser)
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
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	_ = s.Sessions.Destroy(r.Context())
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

func (s *Server) dashboard(w http.ResponseWriter, r *http.Request) {
	user := s.currentUser(r.Context())
	meters, err := s.DB.MeteringPoints(r.Context(), &user)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.render(w, r, http.StatusOK, views.Dashboard(user, meters, s.takeFlash(r.Context())))
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
	s.render(w, r, http.StatusOK, views.Admin(user, meters, users, s.takeFlash(r.Context())))
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
		if err := s.DB.UpdatePassword(r.Context(), id, hash); err != nil {
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

func (s *Server) requireLogin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := s.sessionUserID(r)
		if id == 0 {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		user, err := s.DB.UserByID(r.Context(), id)
		if err != nil || !user.Active {
			_ = s.Sessions.Destroy(r.Context())
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), userKey{}, user)))
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

func (s *Server) sessionUserID(r *http.Request) int64 {
	v := s.Sessions.Get(r.Context(), "user_id")
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

func (s *Server) currentUser(ctx context.Context) db.User {
	user, _ := ctx.Value(userKey{}).(db.User)
	return user
}

func (s *Server) render(w http.ResponseWriter, r *http.Request, status int, component templ.Component) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	if err := component.Render(r.Context(), w); err != nil {
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

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func (s *Server) writeJSONError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}
