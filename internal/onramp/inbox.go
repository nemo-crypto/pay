package onramp

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// migrateInbox 保留已有收件箱数据，为旧数据库补充可恢复的重试字段。
func migrateInbox(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	rows, err := tx.Query("PRAGMA table_info(onramp_events)")
	if err != nil {
		return err
	}
	columns := map[string]bool{}
	for rows.Next() {
		var cid, notnull, pk int
		var name, kind string
		var defaultValue any
		if err = rows.Scan(&cid, &name, &kind, &notnull, &defaultValue, &pk); err != nil {
			rows.Close()
			return err
		}
		columns[name] = true
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, column := range []struct{ name, definition string }{{"attempts", "INTEGER NOT NULL DEFAULT 0"}, {"next_attempt", "INTEGER NOT NULL DEFAULT 0"}, {"last_error", "TEXT NOT NULL DEFAULT ''"}, {"dead_letter", "INTEGER NOT NULL DEFAULT 0"}} {
		if !columns[column.name] {
			if _, err = tx.Exec("ALTER TABLE onramp_events ADD COLUMN " + column.name + " " + column.definition); err != nil {
				return err
			}
		}
	}
	if _, err = tx.Exec("CREATE INDEX IF NOT EXISTS onramp_inbox_retry ON onramp_events(processed,dead_letter,next_attempt,created)"); err != nil {
		return err
	}
	return tx.Commit()
}

type inboxJob struct {
	Provider, OrderID, Raw, ID string
	Attempts                   int
}

func (e *Engine) deferEvent(ctx context.Context, j inboxJob, cause error) error {
	permanent := errors.Is(cause, ErrConflict) || errors.Is(cause, ErrNotFound) || errors.Is(cause, errInvalidEvent) || errors.Is(cause, errSnapshotMismatch)
	code := "processing_unavailable"
	if errors.Is(cause, ErrConflict) {
		code = "provider_reference_conflict"
	}
	if errors.Is(cause, ErrNotFound) {
		code = "order_not_found"
	}
	if errors.Is(cause, errInvalidEvent) {
		code = "invalid_event_data"
	}
	if errors.Is(cause, errSnapshotMismatch) {
		code = "provider_fields_mismatch"
	}
	delay := 30 * time.Second
	for i := 0; i < j.Attempts && delay < time.Hour; i++ {
		delay *= 2
	}
	if delay > time.Hour {
		delay = time.Hour
	}
	_, err := e.DB.ExecContext(ctx, "UPDATE onramp_events SET attempts=attempts+1,next_attempt=?,last_error=?,dead_letter=? WHERE provider=? AND event_id=? AND processed=0", time.Now().Add(delay).Unix(), code, permanent, j.Provider, j.ID)
	return err
}

var errInvalidEvent = errors.New("invalid stored event")

var errSnapshotMismatch = errors.New("provider snapshot does not match settled order")
