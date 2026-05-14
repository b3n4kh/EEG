package views

import (
	"context"
	"fmt"
	"html"
	"io"
	"net/url"
	"strings"
	"time"

	"github.com/a-h/templ"

	"github.com/ben/eeg-sumsum/internal/db"
)

type Flash struct {
	Kind    string
	Message string
}

type UserForm struct {
	User       db.User
	Assigned   map[string]bool
	NewUser    bool
	Error      string
	PasswordOn bool
}

type impersonatorKey struct{}

func WithImpersonator(ctx context.Context, user db.User) context.Context {
	return context.WithValue(ctx, impersonatorKey{}, user)
}

func Login(errorMessage string) templ.Component {
	return page("Login", db.User{}, func(b *strings.Builder) {
		if errorMessage != "" {
			flash(b, Flash{Kind: "error", Message: errorMessage})
		}
		b.WriteString(`<section class="auth-panel"><h1>EEG Portal</h1><form method="post" action="/login" class="stack">`)
		b.WriteString(`<label>Benutzername<input name="username" autocomplete="username" required></label>`)
		b.WriteString(`<label>Passwort<input name="password" type="password" autocomplete="current-password" required></label>`)
		b.WriteString(`<button type="submit">Einloggen</button></form></section>`)
	})
}

func PasswordChange(user db.User, errorMessage string) templ.Component {
	return page("Passwort ändern", user, func(b *strings.Builder) {
		if errorMessage != "" {
			flash(b, Flash{Kind: "error", Message: errorMessage})
		}
		b.WriteString(`<section class="auth-panel"><h1>Passwort ändern</h1>`)
		if user.PasswordChangeRequired {
			b.WriteString(`<p>Bitte ein neues Passwort setzen, bevor das Portal weiter genutzt wird.</p>`)
		}
		b.WriteString(`<form method="post" action="/password/change" class="stack">`)
		b.WriteString(`<label>Aktuelles Passwort<input name="current_password" type="password" autocomplete="current-password" required></label>`)
		b.WriteString(`<label>Neues Passwort<input name="password" type="password" autocomplete="new-password" minlength="10" required></label>`)
		b.WriteString(`<label>Neues Passwort wiederholen<input name="password_confirm" type="password" autocomplete="new-password" minlength="10" required></label>`)
		b.WriteString(`<button type="submit">Passwort speichern</button></form></section>`)
	})
}

func Dashboard(user db.User, meters []db.MeterOverview, participantSummaries []db.ParticipantMeterSummary, flashMsg Flash) templ.Component {
	return page("Dashboard", user, func(b *strings.Builder) {
		flash(b, flashMsg)
		if !user.IsAdmin() {
			participantDashboard(b, participantSummaries)
			b.WriteString(`<section><h2>Zählpunkte</h2>`)
			meterCards(b, meters)
			b.WriteString(`</section>`)
			return
		}
		b.WriteString(`<div class="toolbar"><div><h1>Dashboard</h1><p>Verfügbare Zählpunkte und aktuelle Summen.</p></div>`)
		if user.IsAdmin() {
			b.WriteString(`<a class="button secondary" href="/admin">Administration</a>`)
		}
		b.WriteString(`</div>`)
		meterCards(b, meters)
	})
}

func participantDashboard(b *strings.Builder, summaries []db.ParticipantMeterSummary) {
	b.WriteString(`<div class="toolbar"><div><h1>Dashboard</h1><p>Summen je Zählpunkt im betrachteten Zeitraum.</p></div></div>`)
	if len(summaries) == 0 {
		b.WriteString(`<div class="empty">Noch keine Verbrauchsdaten verfügbar.</div>`)
		return
	}
	b.WriteString(`<div class="summary-grid">`)
	for _, summary := range summaries {
		meterSummaryCard(b, summary)
	}
	b.WriteString(`</div>`)
}

func meterSummaryCard(b *strings.Builder, summary db.ParticipantMeterSummary) {
	fmt.Fprintf(b, `<section class="summary-card"><h2>%s</h2>`, esc(displayMeter(db.MeterOverview{MeteringPoint: summary.MeteringPoint})))
	if p := period(summary.From, summary.To); p != "" {
		fmt.Fprintf(b, `<p>%s</p>`, esc(p))
	}
	b.WriteString(`<dl>`)
	fmt.Fprintf(b, `<dt>%s</dt><dd>%.3f kWh</dd>`, esc(db.MetricCommunityShareLabel), summary.CommunityShareKWh)
	fmt.Fprintf(b, `<dt>%s</dt><dd>%.3f kWh</dd>`, esc(db.MetricTotalConsumptionLabel), summary.TotalConsumptionKWh)
	b.WriteString(`<dt>Deckung</dt>`)
	fmt.Fprintf(b, `<dd>%.1f%%</dd>`, summary.CoveragePercent)
	b.WriteString(`</dl>`)
	chartPercent := summary.CoveragePercent
	if chartPercent < 0 {
		chartPercent = 0
	}
	if chartPercent > 100 {
		chartPercent = 100
	}
	fmt.Fprintf(b, `<div class="coverage-chart" role="img" aria-label="%.1f Prozent Deckung"><span style="width:%.1f%%"></span></div>`, summary.CoveragePercent, chartPercent)
	b.WriteString(`</section>`)
}

func Meter(user db.User, meter db.MeterOverview, metrics []db.MetricTotal, selectedMetric string, chartSVG string, points []db.SeriesPoint) templ.Component {
	return page(meter.ID, user, func(b *strings.Builder) {
		fmt.Fprintf(b, `<a class="back" href="/">Zurück</a><div class="toolbar"><div><h1>%s</h1><p>%s %s</p></div></div>`, esc(displayMeter(meter)), esc(meter.Direction), esc(period(meter.From, meter.To)))
		b.WriteString(`<form method="get" class="filter"><label>Messgröße<select name="metric" onchange="this.form.submit()">`)
		for _, metric := range metrics {
			selected := ""
			if metric.Key == selectedMetric {
				selected = ` selected`
			}
			fmt.Fprintf(b, `<option value="%s"%s>%s</option>`, url.QueryEscape(metric.Key), selected, esc(metric.Label))
		}
		b.WriteString(`</select></label><button type="submit">Anzeigen</button></form>`)
		fmt.Fprintf(b, `<div class="chart-wrap">%s</div>`, chartSVG)
		b.WriteString(`<section><h2>Letzte Messwerte</h2><table><thead><tr><th>Zeitpunkt</th><th>Wert</th></tr></thead><tbody>`)
		start := len(points) - 48
		if start < 0 {
			start = 0
		}
		for i := len(points) - 1; i >= start; i-- {
			fmt.Fprintf(b, `<tr><td>%s</td><td class="num">%.3f kWh</td></tr>`, esc(points[i].IntervalStart.Local().Format("02.01.2006")), points[i].Value)
		}
		b.WriteString(`</tbody></table></section>`)
	})
}

func Admin(user db.User, meters []db.MeterOverview, users []db.User, flashMsg Flash, edaEnabled bool) templ.Component {
	return page("Administration", user, func(b *strings.Builder) {
		flash(b, flashMsg)
		b.WriteString(`<div class="toolbar"><div><h1>Administration</h1><p>Alle Zählpunkte, Benutzer und Upload-Schnittstelle.</p></div><a class="button" href="/admin/users/new">Benutzer anlegen</a></div>`)
		b.WriteString(`<section><h2>Zählpunkte</h2>`)
		meterCards(b, meters)
		b.WriteString(`</section><section><h2>Benutzer</h2><table><thead><tr><th>Name</th><th>Login</th><th>Rolle</th><th>Status</th><th></th></tr></thead><tbody>`)
		for _, u := range users {
			status := "aktiv"
			if !u.Active {
				status = "inaktiv"
			}
			if u.PasswordChangeRequired {
				status += ", Passwortwechsel"
			}
			fmt.Fprintf(b, `<tr><td>%s</td><td>%s</td><td>%s</td><td>%s</td><td><div class="actions"><a href="/admin/users/%d">Bearbeiten</a>`, esc(u.DisplayName), esc(u.Username), esc(u.Role), status, u.ID)
			if u.Active {
				fmt.Fprintf(b, `<form method="post" action="/admin/users/%d/impersonate"><button class="link" type="submit">Übernehmen</button></form>`, u.ID)
			}
			b.WriteString(`</div></td></tr>`)
		}
		b.WriteString(`</tbody></table></section>`)
		b.WriteString(`<section><h2>XLSX Upload</h2>`)
		b.WriteString(`<form method="post" action="/admin/imports" enctype="multipart/form-data" class="filter">`)
		b.WriteString(`<label>Datei<input type="file" name="file" accept=".xlsx,application/vnd.openxmlformats-officedocument.spreadsheetml.sheet" required></label>`)
		b.WriteString(`<button type="submit">XLSX importieren</button></form>`)
		b.WriteString(`<p>API: <code>POST /api/admin/imports</code> mit <code>Authorization: Bearer &lt;token&gt;</code> und Multipart-Feld <code>file</code>.</p></section>`)
		b.WriteString(`<section><h2>EDA Import</h2>`)
		if edaEnabled {
			today := time.Now().AddDate(0, 0, -1).Format("2006-01-02")
			b.WriteString(`<form method="post" action="/admin/eda-imports" class="filter">`)
			fmt.Fprintf(b, `<label>Von<input type="date" name="from" value="%s" required></label>`, esc(today))
			fmt.Fprintf(b, `<label>Bis<input type="date" name="to" value="%s" required></label>`, esc(today))
			b.WriteString(`<button type="submit">EDA importieren</button></form>`)
			b.WriteString(`<p>API: <code>POST /api/admin/eda-imports</code> mit <code>Authorization: Bearer &lt;token&gt;</code> und optionalem JSON <code>{"from":"YYYY-MM-DD","to":"YYYY-MM-DD"}</code>.</p>`)
		} else {
			b.WriteString(`<p>EDA Import ist nicht konfiguriert. Erforderlich sind <code>EDA_USERNAME</code>, <code>EDA_PASSWORD</code> und <code>EDA_COMMUNITY_ID</code>.</p>`)
		}
		b.WriteString(`</section>`)
	})
}

func UserEdit(user db.User, form UserForm, meters []db.MeterOverview) templ.Component {
	title := "Benutzer bearbeiten"
	action := fmt.Sprintf("/admin/users/%d", form.User.ID)
	if form.NewUser {
		title = "Benutzer anlegen"
		action = "/admin/users"
	}
	return page(title, user, func(b *strings.Builder) {
		if form.Error != "" {
			flash(b, Flash{Kind: "error", Message: form.Error})
		}
		fmt.Fprintf(b, `<a class="back" href="/admin">Zurück</a><h1>%s</h1><form method="post" action="%s" class="stack wide">`, esc(title), action)
		if form.NewUser {
			fmt.Fprintf(b, `<label>Login<input name="username" value="%s" required></label>`, esc(form.User.Username))
		}
		fmt.Fprintf(b, `<label>Anzeigename<input name="display_name" value="%s" required></label>`, esc(form.User.DisplayName))
		b.WriteString(`<label>Rolle<select name="role">`)
		for _, role := range []string{db.RoleParticipant, db.RoleAdmin} {
			selected := ""
			if form.User.Role == role {
				selected = ` selected`
			}
			fmt.Fprintf(b, `<option value="%s"%s>%s</option>`, role, selected, role)
		}
		b.WriteString(`</select></label>`)
		checked := ""
		if form.User.Active || form.NewUser {
			checked = ` checked`
		}
		fmt.Fprintf(b, `<label class="check"><input type="checkbox" name="active" value="1"%s> Aktiv</label>`, checked)
		pwLabel := "Neues Passwort"
		required := ""
		if form.NewUser {
			pwLabel = "Passwort"
			required = " required"
		}
		fmt.Fprintf(b, `<label>%s<input name="password" type="password" autocomplete="new-password"%s></label>`, pwLabel, required)
		b.WriteString(`<fieldset><legend>Zugewiesene Zählpunkte</legend><div class="checks">`)
		for _, meter := range meters {
			if meter.ID == "TOTAL" {
				continue
			}
			checked := ""
			if form.Assigned[meter.ID] {
				checked = ` checked`
			}
			fmt.Fprintf(b, `<label class="check"><input type="checkbox" name="meters" value="%s"%s> %s</label>`, esc(meter.ID), checked, esc(displayMeter(meter)))
		}
		b.WriteString(`</div></fieldset><button type="submit">Speichern</button></form>`)
	})
}

func NotFound(user db.User) templ.Component {
	return page("Nicht gefunden", user, func(b *strings.Builder) {
		b.WriteString(`<h1>Nicht gefunden</h1><p>Die angeforderte Seite oder Ressource existiert nicht.</p>`)
	})
}

func ErrorPage(user db.User, message string) templ.Component {
	return page("Fehler", user, func(b *strings.Builder) {
		fmt.Fprintf(b, `<h1>Fehler</h1><p>%s</p>`, esc(message))
	})
}

func page(title string, user db.User, body func(*strings.Builder)) templ.Component {
	return templ.ComponentFunc(func(ctx context.Context, w io.Writer) error {
		var b strings.Builder
		impersonator, impersonating := ctx.Value(impersonatorKey{}).(db.User)
		b.WriteString(`<!doctype html><html lang="de"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">`)
		fmt.Fprintf(&b, `<title>%s</title><style>%s</style></head><body>`, esc(title), css)
		b.WriteString(`<header><a class="brand" href="/">EEG Sumsum</a><nav>`)
		if user.ID != 0 {
			if impersonating && impersonator.ID != 0 {
				fmt.Fprintf(&b, `<span>Als %s</span>`, esc(user.DisplayName))
				fmt.Fprintf(&b, `<form method="post" action="/impersonation/stop"><button class="link" type="submit">Zurück zu %s</button></form>`, esc(impersonator.DisplayName))
			} else {
				fmt.Fprintf(&b, `<span>%s</span>`, esc(user.DisplayName))
			}
			if user.IsAdmin() {
				b.WriteString(`<a href="/admin">Admin</a>`)
			}
			b.WriteString(`<form method="post" action="/logout"><button class="link" type="submit">Logout</button></form>`)
		}
		b.WriteString(`</nav></header><main>`)
		body(&b)
		b.WriteString(`</main></body></html>`)
		_, err := io.WriteString(w, b.String())
		return err
	})
}

func meterCards(b *strings.Builder, meters []db.MeterOverview) {
	if len(meters) == 0 {
		b.WriteString(`<div class="empty">Noch keine Zählpunkte verfügbar.</div>`)
		return
	}
	b.WriteString(`<div class="grid">`)
	for _, meter := range meters {
		fmt.Fprintf(b, `<a class="card" href="/meters/%s"><strong>%s</strong><span>%s</span><span>%s</span>`, url.PathEscape(meter.ID), esc(displayMeter(meter)), esc(meter.Direction), esc(period(meter.From, meter.To)))
		if len(meter.MetricTotals) > 0 {
			b.WriteString(`<dl>`)
			for i, total := range meter.MetricTotals {
				if i >= 3 {
					break
				}
				fmt.Fprintf(b, `<dt>%s</dt><dd>%.3f kWh</dd>`, esc(shortMetric(total.Label)), total.Sum)
			}
			b.WriteString(`</dl>`)
		}
		b.WriteString(`</a>`)
	}
	b.WriteString(`</div>`)
}

func flash(b *strings.Builder, f Flash) {
	if f.Message == "" {
		return
	}
	kind := f.Kind
	if kind == "" {
		kind = "info"
	}
	fmt.Fprintf(b, `<div class="flash %s">%s</div>`, esc(kind), esc(f.Message))
}

func displayMeter(m db.MeterOverview) string {
	if m.DisplayName != "" {
		return m.DisplayName + " (" + m.ID + ")"
	}
	return m.ID
}

func period(from, to *time.Time) string {
	if from == nil || to == nil {
		return ""
	}
	return from.Local().Format("02.01.2006") + " - " + to.Local().Format("02.01.2006")
}

func shortMetric(label string) string {
	label = strings.TrimSuffix(label, " [KWH]")
	if len(label) <= 46 {
		return label
	}
	return label[:43] + "..."
}

func esc(s string) string {
	return html.EscapeString(s)
}

const css = `
:root{color-scheme:light;--bg:#f5f7f8;--panel:#fff;--text:#1e2a32;--muted:#60707b;--line:#d8e0e5;--accent:#0f766e;--accent-2:#134e4a;--danger:#b42318}
*{box-sizing:border-box}body{margin:0;background:var(--bg);color:var(--text);font-family:Inter,ui-sans-serif,system-ui,-apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif;line-height:1.45}
header{height:64px;display:flex;align-items:center;justify-content:space-between;padding:0 28px;background:#fff;border-bottom:1px solid var(--line);position:sticky;top:0;z-index:2}
.brand{font-weight:800;color:var(--accent-2);text-decoration:none}nav{display:flex;gap:16px;align-items:center;color:var(--muted)}nav a{color:var(--accent-2);text-decoration:none}main{max-width:1160px;margin:0 auto;padding:32px 22px 64px}
h1{font-size:34px;line-height:1.1;margin:0 0 8px}h2{font-size:22px;margin:32px 0 14px}p{color:var(--muted);margin:0 0 16px}.toolbar{display:flex;justify-content:space-between;gap:16px;align-items:flex-start;margin-bottom:22px}
.summary-grid{display:grid;grid-template-columns:repeat(auto-fit,minmax(320px,1fr));gap:14px;margin-bottom:26px}.summary-card{background:var(--panel);border:1px solid var(--line);border-radius:8px;padding:18px;min-height:220px}.summary-card h2{font-size:18px;margin:0 0 6px;overflow-wrap:anywhere}.summary-card p{font-size:14px;margin-bottom:14px}.coverage-chart{height:14px;border-radius:999px;background:#e6edf0;overflow:hidden;margin-top:16px}.coverage-chart span{display:block;height:100%;background:var(--accent)}
.grid{display:grid;grid-template-columns:repeat(auto-fit,minmax(270px,1fr));gap:14px}.card{display:block;background:var(--panel);border:1px solid var(--line);border-radius:8px;padding:16px;color:inherit;text-decoration:none;min-height:160px}.card:hover{border-color:var(--accent);box-shadow:0 10px 24px rgba(15,118,110,.08)}.card strong{display:block;overflow-wrap:anywhere}.card span{display:block;color:var(--muted);font-size:14px;margin-top:4px}
dl{display:grid;grid-template-columns:1fr auto;gap:6px 10px;margin:14px 0 0}dt{color:var(--muted);font-size:13px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}dd{margin:0;font-weight:700;font-variant-numeric:tabular-nums}.summary-card dt{white-space:normal;overflow:visible;text-overflow:clip}
.auth-panel{max-width:420px;margin:8vh auto;background:#fff;border:1px solid var(--line);border-radius:8px;padding:28px}.stack{display:grid;gap:14px}.wide{max-width:740px}label{display:grid;gap:6px;font-weight:650}input,select{width:100%;border:1px solid var(--line);border-radius:8px;padding:10px 12px;font:inherit;background:#fff}button,.button{border:0;border-radius:8px;background:var(--accent);color:#fff;padding:10px 14px;font-weight:750;text-decoration:none;cursor:pointer}.secondary{background:#e6f2f0;color:var(--accent-2)}.link{background:transparent;color:var(--accent-2);padding:0}.back{display:inline-block;margin-bottom:18px;color:var(--accent-2);text-decoration:none}
table{width:100%;border-collapse:collapse;background:#fff;border:1px solid var(--line);border-radius:8px;overflow:hidden}th,td{text-align:left;padding:10px 12px;border-bottom:1px solid var(--line)}th{font-size:13px;color:var(--muted);background:#f9fbfb}.num{text-align:right;font-variant-numeric:tabular-nums}.actions{display:flex;gap:12px;align-items:center;flex-wrap:wrap}.actions form{display:inline}
.filter{display:flex;gap:12px;align-items:end;margin:20px 0}.chart-wrap{background:#fff;border:1px solid var(--line);border-radius:8px;padding:12px;overflow-x:auto}.chart{width:100%;min-width:640px;height:auto}.axis{stroke:#c7d2d8;stroke-width:1}.line{fill:none;stroke:var(--accent);stroke-width:3}.tick{fill:#60707b;font-size:12px}.chart-title{fill:#1e2a32;font-weight:700;font-size:14px}.empty{background:#fff;border:1px dashed var(--line);border-radius:8px;padding:18px;color:var(--muted)}
.flash{border-radius:8px;padding:12px 14px;margin-bottom:18px;background:#e6f2f0;color:#134e4a}.flash.error{background:#fee4e2;color:var(--danger)}fieldset{border:1px solid var(--line);border-radius:8px;padding:14px}.checks{display:grid;grid-template-columns:repeat(auto-fit,minmax(260px,1fr));gap:8px}.check{display:flex;align-items:center;gap:8px;font-weight:500}.check input{width:auto}
@media (max-width:700px){header{padding:0 16px}main{padding:22px 14px 48px}.toolbar,.filter{display:grid}h1{font-size:28px}}
`
