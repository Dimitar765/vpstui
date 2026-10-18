package apitypes

import "time"

// BackupEntry is one file in a managed database's backup directory
// (GET /api/databases/{id}/backups). Entries arrive newest first.
type BackupEntry struct {
	Name     string    `json:"name"`
	Size     int64     `json:"size"`
	Modified time.Time `json:"modified"`
}

// PerfRow is one container's row of a server's perf ranking. Field names
// mirror internal/httpapi's perfRowJSON so the GUI's perf table renders both
// feeds; only the fields derivable from the monitor's latest snapshot are
// filled server-side (no window history / cgroup peak over this transport).
type PerfRow struct {
	ID       string  `json:"id"`
	Name     string  `json:"name"`
	CPU      float64 `json:"cpu"`                // latest cpu, percent of one core
	MemPct   float64 `json:"memPct"`             // latest docker memory percentage
	MemUsage string  `json:"memUsage,omitempty"` // docker's "used / limit" text
	State    string  `json:"state,omitempty"`
}

// PerfSourceSnapshot marks a perf section ranked from the monitor's latest
// in-memory sample rather than a windowed probe.
const PerfSourceSnapshot = "snapshot"

// ServerPerf is one server's perf section on a fleet row (the "perf" field).
// Rows are ranked cpu desc (ties by name) and never null.
type ServerPerf struct {
	Source string    `json:"source"`
	Rows   []PerfRow `json:"rows"`
}
