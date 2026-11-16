package session

import "time"

// LatestReport is the list projection of a session report. It is nil when the
// session has no report yet; the list API must not synthesize a zero score.
type LatestReport struct {
	TotalScore  float64
	ScoreBand   string
	GeneratedAt time.Time
}

// Summary is one row of the history list: the session plus its newest report.
// Embedding keeps every existing session field available without a second
// lookup, so the list stays a single query.
type Summary struct {
	Session
	LatestReport *LatestReport
}
