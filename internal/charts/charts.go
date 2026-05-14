package charts

import (
	"fmt"
	"html"
	"math"
	"strings"

	"github.com/ben/eeg-sumsum/internal/db"
)

func LineSVG(points []db.SeriesPoint, title string) string {
	if len(points) == 0 {
		return `<div class="empty">Keine Messwerte für diese Messgröße.</div>`
	}
	const width = 900
	const height = 280
	const padX = 44
	const padY = 28
	min, max := points[0].Value, points[0].Value
	for _, p := range points {
		min = math.Min(min, p.Value)
		max = math.Max(max, p.Value)
	}
	if min == max {
		max = min + 1
	}
	scaleX := float64(width-2*padX) / math.Max(1, float64(len(points)-1))
	scaleY := float64(height-2*padY) / (max - min)
	var path strings.Builder
	for i, p := range points {
		x := float64(padX) + float64(i)*scaleX
		y := float64(height-padY) - (p.Value-min)*scaleY
		if i == 0 {
			fmt.Fprintf(&path, "M %.1f %.1f", x, y)
		} else {
			fmt.Fprintf(&path, " L %.1f %.1f", x, y)
		}
	}
	first := points[0].IntervalStart.Local().Format("02.01 15:04")
	last := points[len(points)-1].IntervalStart.Local().Format("02.01 15:04")
	return fmt.Sprintf(`<svg class="chart" viewBox="0 0 %d %d" role="img" aria-label="%s">
  <line x1="%d" y1="%d" x2="%d" y2="%d" class="axis"/>
  <line x1="%d" y1="%d" x2="%d" y2="%d" class="axis"/>
  <text x="%d" y="18" class="chart-title">%s</text>
  <text x="%d" y="%d" class="tick">%.3f kWh</text>
  <text x="%d" y="%d" class="tick">%.3f kWh</text>
  <text x="%d" y="%d" class="tick">%s</text>
  <text x="%d" y="%d" text-anchor="end" class="tick">%s</text>
  <path d="%s" class="line"/>
</svg>`, width, height, html.EscapeString(title),
		padX, height-padY, width-padX, height-padY,
		padX, padY, padX, height-padY,
		padX, html.EscapeString(title),
		padX+4, padY+8, max,
		padX+4, height-padY-6, min,
		padX, height-6, html.EscapeString(first),
		width-padX, height-6, html.EscapeString(last),
		path.String())
}
