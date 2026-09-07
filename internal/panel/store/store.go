// Package store 是面板的持久化层：SQLite 单文件（cgo-free 驱动 modernc.org/sqlite），
// 保持面板单二进制、零外部依赖。所有 SQL 集中在本包；上层只见 Go 类型。
package store

import (
	"context"
	"database/sql"
	_ "embed"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

//go:embed schema.sql
var schemaSQL string

// ErrNotFound 记录不存在。
var ErrNotFound = errors.New("store: not found")

// IsConflict 唯一约束冲突（如集群名重复）。
func IsConflict(err error) bool {
	var se *sqlite.Error
	return errors.As(err, &se) && se.Code() == sqlite3.SQLITE_CONSTRAINT_UNIQUE
}

// Store SQLite 存储。方法并发安全（连接池限一条写连接，SQLite 本就单写者）。
type Store struct {
	db *sql.DB
}

// Open 打开（不存在则建）数据库文件并初始化 schema。path 为空用内存库（仅测试）。
func Open(path string) (*Store, error) {
	var dsn string
	if path == "" {
		dsn = "file::memory:?_pragma=foreign_keys(1)"
	} else {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return nil, fmt.Errorf("store: 建目录: %w", err)
		}
		dsn = "file:" + filepath.ToSlash(path) +
			"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)&_pragma=synchronous(NORMAL)"
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// SQLite 单写者：一条连接足够，也避免 database is locked
	db.SetMaxOpenConns(1)
	db.SetConnMaxLifetime(0)
	if _, err := db.Exec(schemaSQL); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("store: 初始化 schema: %w", err)
	}
	st := &Store{db: db}
	if err := st.migrate(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("store: 迁移: %w", err)
	}
	return st, nil
}

// migrations 对已有表的增量列变更：schema.sql 只负责建表，加列在这里（ADD COLUMN 不支持 IF NOT EXISTS）。
var migrations = []struct{ table, column, ddl string }{
	{"nodes", "machine_id", `ALTER TABLE nodes ADD COLUMN machine_id TEXT NOT NULL DEFAULT ''`},
	{"clusters", "source", `ALTER TABLE clusters ADD COLUMN source TEXT NOT NULL DEFAULT 'kubeadm'`},
}

func (s *Store) migrate() error {
	for _, m := range migrations {
		has, err := s.hasColumn(m.table, m.column)
		if err != nil {
			return err
		}
		if !has {
			if _, err := s.db.Exec(m.ddl); err != nil {
				return fmt.Errorf("%s.%s: %w", m.table, m.column, err)
			}
		}
	}
	// 加列后再建依赖该列的索引
	_, err := s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_nodes_machine ON nodes(machine_id)`)
	return err
}

func (s *Store) hasColumn(table, column string) (bool, error) {
	rows, err := s.db.Query(`PRAGMA table_info(` + table + `)`)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, typ string
		var notnull, pk int
		var dflt any
		if err := rows.Scan(&cid, &name, &typ, &notnull, &dflt, &pk); err != nil {
			return false, err
		}
		if name == column {
			return true, nil
		}
	}
	return false, rows.Err()
}

// Close 关闭数据库。
func (s *Store) Close() error { return s.db.Close() }

// Backup 用 VACUUM INTO 生成一致性快照（面板运行中亦可，WAL 下不阻塞写）。目标文件须不存在。
func (s *Store) Backup(ctx context.Context, path string) error {
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("store: 目标已存在: %s", path)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `VACUUM INTO ?`, filepath.ToSlash(path))
	return err
}

// ── 时间编码：统一 RFC3339Nano UTC 文本，空串表示零值 ──

func ts(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339Nano)
}

func parseTS(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	t, _ := time.Parse(time.RFC3339Nano, s)
	return t
}

func now() string { return ts(time.Now()) }

// tx 在事务里执行 fn。
func (s *Store) tx(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}
