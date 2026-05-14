package displayfmt

import (
	"fmt"
	"strings"
)

func Decimal(format string, value float64) string {
	return strings.ReplaceAll(fmt.Sprintf(format, value), ".", ",")
}

func KWh(value float64) string {
	return Decimal("%.2f", value) + " kWh"
}

func Percent1(value float64) string {
	return Decimal("%.1f", value) + "%"
}
