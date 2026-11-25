// Package db 负责打开数据库连接（SQLite 默认 / PostgreSQL 兼容）并保证表结构就绪。
package db

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"cmd2api/ent"
	"cmd2api/internal/config"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	_ "github.com/lib/pq"
	_ "modernc.org/sqlite"
)

// Open 建立连接池、验证连通性，并把表结构迁移到当前 schema。
//
// 默认使用纯 Go SQLite（modernc.org/sqlite）实现零外部依赖开箱即用；
// 显式指定 Postgres 或配置了 DB_PASSWORD 时切换到 PostgreSQL。
func Open(ctx context.Context, cfg config.DatabaseConfig) (*ent.Client, error) {
	driver := strings.ToLower(strings.TrimSpace(cfg.Driver))
	if driver == "" {
		if cfg.Password != "" {
			driver = "postgres"
		} else {
			driver = "sqlite"
		}
	}

	switch driver {
	case "sqlite", "sqlite3":
		return openSQLite(ctx, cfg)
	case "postgres", "postgresql", "pg":
		return openPostgres(ctx, cfg)
	default:
		return nil, fmt.Errorf("不支持的数据库驱动: %q", cfg.Driver)
	}
}

func openSQLite(ctx context.Context, cfg config.DatabaseConfig) (*ent.Client, error) {
	sqlitePath := strings.TrimSpace(cfg.SQLitePath)
	if sqlitePath == "" {
		sqlitePath = "./data/analy2api.db"
	}

	if !isMemorySQLite(sqlitePath) {
		dir := filepath.Dir(sqlitePath)
		if dir != "" && dir != "." {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return nil, fmt.Errorf("创建 SQLite 数据目录 %s: %w", dir, err)
			}
		}
	}

	dsn := buildSQLiteDSN(sqlitePath)
	sqlDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("打开 SQLite 数据库: %w", err)
	}
	// 单写连接避免并发写导致 SQLITE_BUSY，同时保证 :memory: 始终复用同一内存库。
	sqlDB.SetMaxOpenConns(1)
	sqlDB.SetMaxIdleConns(1)

	if err := sqlDB.PingContext(ctx); err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("探测 SQLite 连通性: %w", err)
	}

	drv := entsql.OpenDB(dialect.SQLite, sqlDB)
	client := ent.NewClient(ent.Driver(drv))
	if err := client.Schema.Create(ctx); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("迁移表结构: %w", err)
	}
	return client, nil
}

func openPostgres(ctx context.Context, cfg config.DatabaseConfig) (*ent.Client, error) {
	drv, err := entsql.Open(dialect.Postgres, cfg.DSN())
	if err != nil {
		return nil, fmt.Errorf("连接数据库: %w", err)
	}

	pool := drv.DB()
	pool.SetMaxOpenConns(cfg.MaxOpenConns)
	pool.SetMaxIdleConns(cfg.MaxIdleConns)
	pool.SetConnMaxLifetime(time.Hour)

	if err := ping(ctx, pool); err != nil {
		_ = drv.Close()
		return nil, err
	}

	client := ent.NewClient(ent.Driver(drv))
	if err := client.Schema.Create(ctx); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("迁移表结构: %w", err)
	}
	return client, nil
}

func isMemorySQLite(path string) bool {
	return path == ":memory:" ||
		strings.HasPrefix(path, "file::memory:") ||
		strings.Contains(path, "mode=memory")
}

func buildSQLiteDSN(sqlitePath string) string {
	const filePragmas = "_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=busy_timeout(5000)&_pragma=cache_size(-16000)&_pragma=temp_store(MEMORY)"
	const memPragmas = "_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)&_pragma=temp_store(MEMORY)"

	pragmas := filePragmas
	if isMemorySQLite(sqlitePath) {
		pragmas = memPragmas
	}
	if strings.Contains(sqlitePath, "?") {
		if strings.Contains(sqlitePath, "_pragma=") {
			return sqlitePath
		}
		return sqlitePath + "&" + pragmas
	}
	return sqlitePath + "?" + pragmas
}

// ping 带重试地探活。
//
// 容器编排里 app 常常比 postgres 先起来，compose 的 depends_on 只保证启动顺序
// 不保证就绪。这里退避重试，省得靠 restart 策略兜底。
func ping(ctx context.Context, pool *sql.DB) error {
	const attempts = 10
	var lastErr error
	for i := range attempts {
		if err := pool.PingContext(ctx); err == nil {
			return nil
		} else {
			lastErr = err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Duration(i+1) * time.Second):
		}
	}
	return fmt.Errorf("数据库在 %d 次重试后仍不可用: %w", attempts, lastErr)
}
