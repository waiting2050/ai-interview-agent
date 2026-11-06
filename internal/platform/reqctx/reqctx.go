// Package reqctx carries request-scoped identifiers into the application
// layer so every log line can be correlated with an inbound request, run,
// turn and session.
package reqctx

import "context"

type key struct{}

type sessionKey struct{}
type runKey struct{}
type turnKey struct{}

// With attaches the inbound request id.
func With(ctx context.Context, requestID string) context.Context {
	return context.WithValue(ctx, key{}, requestID)
}

// From returns the inbound request id, or "".
func From(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	id, _ := ctx.Value(key{}).(string)
	return id
}

// WithSession attaches the session id.
func WithSession(ctx context.Context, sessionID string) context.Context {
	return context.WithValue(ctx, sessionKey{}, sessionID)
}

// SessionID returns the session id, or "".
func SessionID(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	id, _ := ctx.Value(sessionKey{}).(string)
	return id
}

// WithRun attaches the run id.
func WithRun(ctx context.Context, runID string) context.Context {
	return context.WithValue(ctx, runKey{}, runID)
}

// RunID returns the run id, or "".
func RunID(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	id, _ := ctx.Value(runKey{}).(string)
	return id
}

// WithTurn attaches the turn id.
func WithTurn(ctx context.Context, turnID string) context.Context {
	return context.WithValue(ctx, turnKey{}, turnID)
}

// TurnID returns the turn id, or "".
func TurnID(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	id, _ := ctx.Value(turnKey{}).(string)
	return id
}

// Attrs returns slog key-value pairs for every identifier present in ctx.
// Empty identifiers are skipped so log lines stay stable and readable.
func Attrs(ctx context.Context) []any {
	attrs := make([]any, 0, 8)
	if v := From(ctx); v != "" {
		attrs = append(attrs, "request_id", v)
	}
	if v := SessionID(ctx); v != "" {
		attrs = append(attrs, "session_id", v)
	}
	if v := RunID(ctx); v != "" {
		attrs = append(attrs, "run_id", v)
	}
	if v := TurnID(ctx); v != "" {
		attrs = append(attrs, "turn_id", v)
	}
	return attrs
}
