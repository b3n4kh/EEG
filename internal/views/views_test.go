package views

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/a-h/templ"
	"github.com/stretchr/testify/require"

	"github.com/ben/eeg-sumsum/internal/db"
)

func TestDashboardRendersAdminMetersAndEscapesNames(t *testing.T) {
	html := renderComponent(t, Dashboard(
		db.User{Username: "admin", DisplayName: "Admin", Role: db.RoleAdmin, Active: true},
		[]db.MeterOverview{{MeteringPoint: db.MeteringPoint{ID: `AT<001>`, DisplayName: `Meter & One`, Direction: "GENERATION"}}},
		nil,
		Flash{Message: `Saved & <ok>`},
	))
	require.Contains(t, html, "Administration")
	require.Contains(t, html, "Saved &amp; &lt;ok&gt;")
	require.Contains(t, html, "Meter &amp; One")
	require.NotContains(t, html, `AT<001>`)
}

func TestParticipantDashboardRendersPerMeterSummaryAndMeterLinks(t *testing.T) {
	from := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	to := from.Add(15 * time.Minute)
	html := renderComponent(t, Dashboard(
		db.User{Username: "teilnehmer", DisplayName: "Teilnehmer", Role: db.RoleParticipant, Active: true},
		[]db.MeterOverview{{MeteringPoint: db.MeteringPoint{ID: "AT001", Direction: "CONSUMPTION"}}},
		[]db.ParticipantMeterSummary{{
			MeteringPoint:       db.MeteringPoint{ID: "AT001", Direction: "CONSUMPTION"},
			CommunityShareKWh:   50,
			TotalConsumptionKWh: 200,
			CoveragePercent:     25,
			From:                &from,
			To:                  &to,
			HasData:             true,
		}},
		Flash{},
	))
	for _, want := range []string{"50.000 kWh", "200.000 kWh", "25.0%", `href="/meters/AT001"`, "coverage-chart"} {
		require.Contains(t, html, want)
	}
	require.NotContains(t, html, "Administration")
}

func TestLoginAndPasswordChangeRenderErrors(t *testing.T) {
	login := renderComponent(t, Login("Benutzername oder Passwort ist falsch."))
	require.Contains(t, login, "Benutzername oder Passwort ist falsch.")
	require.Contains(t, login, `autocomplete="username"`)

	password := renderComponent(t, PasswordChange(db.User{DisplayName: "Teilnehmer", PasswordChangeRequired: true}, "Zu kurz."))
	require.Contains(t, password, "Zu kurz.")
	require.Contains(t, password, "Bitte ein neues Passwort setzen")
}

func TestImpersonatorBanner(t *testing.T) {
	var b strings.Builder
	ctx := WithImpersonator(context.Background(), db.User{ID: 1, DisplayName: "Admin"})
	require.NoError(t, page("Test", db.User{ID: 2, DisplayName: "Teilnehmer"}, func(b *strings.Builder) {
		b.WriteString("Body")
	}).Render(ctx, &b))
	require.Contains(t, b.String(), "Als Teilnehmer")
	require.Contains(t, b.String(), "Zurück zu Admin")
}

func renderComponent(t *testing.T, component templ.Component) string {
	t.Helper()
	var b strings.Builder
	require.NoError(t, component.Render(context.Background(), &b))
	return b.String()
}
