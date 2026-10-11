package httpapi

import "vpstui/internal/perf"

// parsePercent converts docker's pre-formatted CPU/MEM percent cells
// ("12.34%", "-", "--") into a number. It is the shared parser, not a second one:
// the terminal colours a cell with this same function, so a reading the page
// plots cannot mean something else on the screen beside it.
func parsePercent(s string) (float64, bool) { return perf.Percent(s) }
