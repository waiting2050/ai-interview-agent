// Package redis holds coordination primitives only: the per-session lock and
// the idempotency fast-path cache. Correctness must never depend on Redis;
// PostgreSQL unique constraints and CAS updates are the source of truth.
package redis
