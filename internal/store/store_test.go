package store

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func TestWorkerIndexesOnExistingDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pay.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	// 模拟升级前的数据库，保留已有账户和未处理回调。
	_, err = s.DB.Exec(`DROP INDEX order_due;
 DROP INDEX inbox_pending;
 INSERT INTO users(id,coins) VALUES('alice',2000);
 INSERT INTO inbox(gateway,id,body_hash,snapshot,created) VALUES('stripe','evt_existing','hash','{}',1);`)
	if err != nil {
		s.DB.Close()
		t.Fatal(err)
	}
	if err := s.DB.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.DB.Close() })
	if coins, _, err := s.Account(context.Background(), "alice"); err != nil || coins != 2000 {
		t.Fatalf("account after upgrade: %d, %v", coins, err)
	}
	if jobs, err := s.Jobs(context.Background()); err != nil || len(jobs) != 1 || jobs[0].ID != "evt_existing" {
		t.Fatalf("inbox after upgrade: %+v, %v", jobs, err)
	}
	for _, tc := range []struct {
		name, query, index string
		args               []any
	}{
		{"orders", `SELECT data FROM orders WHERE next_check<=? AND created>? AND (status='pending' OR (ref IS NOT NULL AND status IN ('failed','expired'))) ORDER BY next_check LIMIT ?`, "order_due", []any{200, 0, 100}},
		{"inbox", `SELECT gateway,id,snapshot FROM inbox WHERE processed=0 AND next_attempt<=? ORDER BY created LIMIT 100`, "inbox_pending", []any{200}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rows, err := s.DB.Query("EXPLAIN QUERY PLAN "+tc.query, tc.args...)
			if err != nil {
				t.Fatal(err)
			}
			defer rows.Close()
			var plan []string
			for rows.Next() {
				var id, parent, unused int
				var detail string
				if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
					t.Fatal(err)
				}
				plan = append(plan, detail)
			}
			if err := rows.Err(); err != nil {
				t.Fatal(err)
			}
			detail := strings.Join(plan, "\n")
			if !strings.Contains(detail, tc.index) || strings.Contains(detail, "USE TEMP B-TREE") {
				t.Fatalf("expected worker index without temporary sorting, got:\n%s", detail)
			}
		})
	}
}
