package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/waiting2050/ai-interview-agent/internal/domain/jd"
)

type jdRepository struct{ q querier }

const jdColumns = `id, source_kind, source_url, fetched_at, raw_text, structured, created_at`

func (r *jdRepository) Save(ctx context.Context, listing *jd.Listing) error {
	structured, err := json.Marshal(listing.Structured)
	if err != nil {
		return fmt.Errorf("marshal jd structured: %w", err)
	}
	_, err = r.q.Exec(ctx, `
		INSERT INTO jd_listings (`+jdColumns+`)
		VALUES ($1,$2,$3,$4,$5,$6,$7)
		ON CONFLICT (id) DO UPDATE SET
			source_url = EXCLUDED.source_url,
			fetched_at = EXCLUDED.fetched_at,
			raw_text = EXCLUDED.raw_text,
			structured = EXCLUDED.structured`,
		listing.ID, listing.SourceKind, listing.SourceURL, listing.FetchedAt,
		listing.RawText, string(structured), listing.CreatedAt)
	if err != nil {
		return fmt.Errorf("save jd listing: %w", err)
	}
	return nil
}

func (r *jdRepository) Get(ctx context.Context, id string) (*jd.Listing, error) {
	row := r.q.QueryRow(ctx, `SELECT `+jdColumns+` FROM jd_listings WHERE id = $1`, id)
	var (
		listing    jd.Listing
		structured []byte
	)
	if err := row.Scan(&listing.ID, &listing.SourceKind, &listing.SourceURL, &listing.FetchedAt,
		&listing.RawText, &structured, &listing.CreatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, jd.ErrNotFound
		}
		return nil, fmt.Errorf("get jd listing: %w", err)
	}
	if err := json.Unmarshal(structured, &listing.Structured); err != nil {
		return nil, fmt.Errorf("unmarshal jd structured: %w", err)
	}
	return &listing, nil
}
