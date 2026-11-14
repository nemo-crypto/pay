package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

var ErrEnvironmentMismatch = errors.New("database payment environment mismatch; use a separate DATABASE_PATH")
var ErrUnscopedDatabase = errors.New("existing database has no verified payment environment; back up and audit it before migration, or use a new DATABASE_PATH")

// EnsureEnvironment 必须在初始化网关、处理回调和启动 Worker 前执行。
// 不猜测历史数据所属环境，避免演示余额、沙箱通知或 token 被带入真实支付。
func (s *Store) EnsureEnvironment(ctx context.Context, environment string) error {
	if environment == "" {
		return fmt.Errorf("payment environment is required")
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS payment_environment(id INTEGER PRIMARY KEY CHECK(id=1),scope TEXT NOT NULL)`); err != nil {
		return err
	}
	var saved string
	err = tx.QueryRowContext(ctx, "SELECT scope FROM payment_environment WHERE id=1").Scan(&saved)
	if err == nil {
		if saved != environment {
			return ErrEnvironmentMismatch
		}
		return tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	// 仅查询固定业务表；旧版可能尚未初始化买币或演示模块。
	for _, table := range []string{"users", "orders", "requests", "ledger", "inbox", "onramp_orders", "onramp_events", "onramp_tokens", "onramp_mock", "demo_payments"} {
		var exists bool
		if err = tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM sqlite_master WHERE type='table' AND name=?)", table).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			continue
		}
		var populated bool
		if err = tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM "+table+")").Scan(&populated); err != nil {
			return err
		}
		if populated {
			return ErrUnscopedDatabase
		}
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO payment_environment(id,scope) VALUES(1,?)", environment); err != nil {
		return err
	}
	return tx.Commit()
}
