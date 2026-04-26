package eventlog

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

type sqliteStore struct {
	db *sql.DB
}

func NewSQLiteStore(path string) (Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("eventlog: open sqlite: %w", err)
	}

	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("eventlog: ping sqlite: %w", err)
	}

	if err := migrate(db); err != nil {
		return nil, fmt.Errorf("eventlog: migrate: %w", err)
	}

	return &sqliteStore{db: db}, nil
}

func migrate(db *sql.DB) error {
	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS event_log (
			id           TEXT PRIMARY KEY,
			aggregate_id TEXT NOT NULL,
			event_type   TEXT NOT NULL,
			payload      TEXT NOT NULL,
			occurred_at  DATETIME NOT NULL
		);
		CREATE INDEX IF NOT EXISTS idx_event_log_aggregate_id ON event_log (aggregate_id);
	`)
	return err
}

func (s *sqliteStore) Append(ctx context.Context, e Event) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO event_log (id, aggregate_id, event_type, payload, occurred_at)
		 VALUES (?, ?, ?, ?, ?)`,
		e.ID.String(),
		e.AggregateID.String(),
		e.EventType,
		string(e.Payload),
		e.OccurredAt.UTC().Format(time.RFC3339Nano),
	)
	if err != nil {
		return fmt.Errorf("eventlog: append %s: %w", e.EventType, err)
	}
	return nil
}
