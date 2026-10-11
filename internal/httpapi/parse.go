package httpapi

import (
	"math"
	"strconv"
	"strings"
)

// parsePercent converts docker's pre-formatted CPU/MEM percent cells
// ("12.34%", "-", "--") into a number. Docker emits "-" before stats are
// available, so unparseable input is normal, not an error.
func parsePercent(s string) (float64, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false
	}
	v, err := strconv.ParseFloat(strings.TrimSuffix(s, "%"), 64)
	// ParseFloat also accepts NaN/Inf, which would poison a width style.
	if err != nil || v < 0 || math.IsNaN(v) || math.IsInf(v, 0) {
		return 0, false
	}
	return v, true
}
