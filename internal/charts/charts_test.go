package charts

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/ben/eeg-sumsum/internal/db"
)

func TestLineSVGEmptyState(t *testing.T) {
	got := LineSVG(nil, "Wirkenergie")
	require.Contains(t, got, "Keine Messwerte")
	require.NotContains(t, got, "<svg")
}

func TestLineSVGEscapesTitleAndRendersPath(t *testing.T) {
	points := []db.SeriesPoint{
		{IntervalStart: time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC), Value: 1.5},
		{IntervalStart: time.Date(2026, 5, 1, 0, 15, 0, 0, time.UTC), Value: 3.0},
		{IntervalStart: time.Date(2026, 5, 1, 0, 30, 0, 0, time.UTC), Value: 2.0},
	}
	got := LineSVG(points, `A&B "quote" <script>`)
	require.Contains(t, got, `<svg class="chart"`)
	require.Contains(t, got, `aria-label="A&amp;B &#34;quote&#34; &lt;script&gt;"`)
	require.Contains(t, got, `A&amp;B &#34;quote&#34; &lt;script&gt;`)
	require.Contains(t, got, `<path d="M `)
	require.GreaterOrEqual(t, strings.Count(got, " L "), 2)
}

func TestLineSVGFlatSeriesExpandsScale(t *testing.T) {
	points := []db.SeriesPoint{
		{IntervalStart: time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC), Value: 7},
		{IntervalStart: time.Date(2026, 5, 1, 0, 15, 0, 0, time.UTC), Value: 7},
	}
	got := LineSVG(points, "Flat")
	require.Contains(t, got, "8,00 kWh")
	require.Contains(t, got, "7,00 kWh")
	require.NotContains(t, got, "NaN")
	require.NotContains(t, got, "+Inf")
}
