package testmysql

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/donnel666/remail/internal/platform"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

type Server struct {
	prefix       string
	once         sync.Once
	container    testcontainers.Container
	host         string
	port         string
	err          error
	nextDB       uint64
	templateOnce sync.Once
	templateName string
	templateErr  error
}

func New(prefix string) *Server {
	return &Server{prefix: prefix}
}

func (s *Server) Database(t *testing.T, migrationsDir string) *gorm.DB {
	t.Helper()

	s.once.Do(func() {
		s.start()
	})
	require.NoError(t, s.err)
	require.NoError(t, s.ensureTemplate(migrationsDir))

	dbName := fmt.Sprintf("%s_%d", s.prefix, atomic.AddUint64(&s.nextDB, 1))
	adminDB, err := openAdminDB(s.host, s.port)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = adminDB.Exec("DROP DATABASE IF EXISTS " + quoteIdentifier(dbName))
		require.NoError(t, adminDB.Close())
	})

	require.NoError(t, adminDB.Ping())
	require.NoError(t, cloneDatabase(adminDB, s.templateName, dbName))

	dsn := testDSN(s.host, s.port, dbName)
	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{TranslateError: true})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, sqlDB.Close())
	})
	return db
}

func (s *Server) Close(ctx context.Context) error {
	if s == nil || s.container == nil {
		return nil
	}
	return s.container.Terminate(ctx)
}

func (s *Server) ensureTemplate(migrationsDir string) error {
	s.templateOnce.Do(func() {
		s.templateName = s.prefix + "_template"
		adminDB, err := openAdminDB(s.host, s.port)
		if err != nil {
			s.templateErr = err
			return
		}
		defer adminDB.Close()

		if err := adminDB.Ping(); err != nil {
			s.templateErr = err
			return
		}
		if err := execSQL(adminDB, "CREATE DATABASE "+quoteIdentifier(s.templateName)+" CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci"); err != nil {
			s.templateErr = err
			return
		}
		if err := execSQL(adminDB, "GRANT ALL PRIVILEGES ON "+quoteIdentifier(s.templateName)+".* TO 'remail'@'%'"); err != nil {
			s.templateErr = err
			return
		}

		dsn := testDSN(s.host, s.port, s.templateName)
		sqlDB, err := sql.Open("mysql", dsn)
		if err != nil {
			s.templateErr = err
			return
		}
		defer sqlDB.Close()

		sqlDB.SetMaxOpenConns(4)
		sqlDB.SetConnMaxLifetime(2 * time.Minute)

		if err := sqlDB.Ping(); err != nil {
			s.templateErr = err
			return
		}
		s.templateErr = platform.RunMigrations(sqlDB, migrationsDir)
	})
	return s.templateErr
}

func (s *Server) start() {
	ctx := context.Background()
	req := testcontainers.ContainerRequest{
		Image:        "mysql:8.0",
		ExposedPorts: []string{"3306/tcp"},
		Env: map[string]string{
			"MYSQL_ROOT_PASSWORD": "root",
			"MYSQL_DATABASE":      "remail_test",
			"MYSQL_USER":          "remail",
			"MYSQL_PASSWORD":      "remail",
		},
		WaitingFor: wait.ForListeningPort("3306/tcp").WithStartupTimeout(2 * time.Minute),
	}

	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: req,
		Started:          true,
	})
	if err != nil {
		s.err = err
		return
	}
	s.container = container

	host, err := container.Host(ctx)
	if err != nil {
		s.err = err
		return
	}
	port, err := container.MappedPort(ctx, "3306/tcp")
	if err != nil {
		s.err = err
		return
	}
	s.host = host
	s.port = port.Port()

	var sqlDB *sql.DB
	var lastErr error
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if sqlDB != nil {
			_ = sqlDB.Close()
		}
		sqlDB, lastErr = openAdminDB(s.host, s.port)
		if lastErr != nil {
			time.Sleep(500 * time.Millisecond)
			continue
		}
		if lastErr = sqlDB.PingContext(ctx); lastErr == nil {
			_ = sqlDB.Close()
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
	if sqlDB != nil {
		_ = sqlDB.Close()
	}
	s.err = fmt.Errorf("mysql did not become ready: %w", lastErr)
}

func openAdminDB(host, port string) (*sql.DB, error) {
	dsn := fmt.Sprintf(
		"root:root@tcp(%s:%s)/?charset=utf8mb4&parseTime=True&loc=Local&multiStatements=true&timeout=30s&readTimeout=120s&writeTimeout=120s",
		host,
		port,
	)
	return sql.Open("mysql", dsn)
}

func testDSN(host, port, dbName string) string {
	return fmt.Sprintf(
		"remail:remail@tcp(%s:%s)/%s?charset=utf8mb4&parseTime=True&loc=Local&timeout=30s&readTimeout=60s&writeTimeout=60s",
		host,
		port,
		dbName,
	)
}

func cloneDatabase(adminDB *sql.DB, source, target string) (err error) {
	if err := execSQL(adminDB, "CREATE DATABASE "+quoteIdentifier(target)+" CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci"); err != nil {
		return err
	}
	if err := execSQL(adminDB, "GRANT ALL PRIVILEGES ON "+quoteIdentifier(target)+".* TO 'remail'@'%'"); err != nil {
		return err
	}

	tables, err := listTables(adminDB, source)
	if err != nil {
		return err
	}
	if len(tables) == 0 {
		return fmt.Errorf("template database %s has no tables", source)
	}

	ctx := context.Background()
	conn, err := adminDB.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()

	if _, err := conn.ExecContext(ctx, "USE "+quoteIdentifier(target)); err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx, "SET FOREIGN_KEY_CHECKS = 0"); err != nil {
		return err
	}
	defer func() {
		if _, restoreErr := conn.ExecContext(ctx, "SET FOREIGN_KEY_CHECKS = 1"); err == nil && restoreErr != nil {
			err = restoreErr
		}
	}()

	sourceRef := quoteIdentifier(source)
	targetRef := quoteIdentifier(target)
	for _, table := range tables {
		tableRef := quoteIdentifier(table)
		var returnedTable, createStatement string
		if err := conn.QueryRowContext(ctx, "SHOW CREATE TABLE "+sourceRef+"."+tableRef).Scan(&returnedTable, &createStatement); err != nil {
			return err
		}
		if _, err := conn.ExecContext(ctx, createStatement); err != nil {
			return err
		}
		columns, err := listInsertableColumns(adminDB, source, table)
		if err != nil {
			return err
		}
		if len(columns) == 0 {
			continue
		}
		columnList := strings.Join(columns, ", ")
		if _, err := conn.ExecContext(ctx, fmt.Sprintf(
			"INSERT INTO %s.%s (%s) SELECT %s FROM %s.%s",
			targetRef,
			tableRef,
			columnList,
			columnList,
			sourceRef,
			tableRef,
		)); err != nil {
			return err
		}
	}
	return nil
}

func listInsertableColumns(adminDB *sql.DB, schema, table string) ([]string, error) {
	rows, err := adminDB.Query(`
SELECT column_name
FROM information_schema.columns
WHERE table_schema = ?
  AND table_name = ?
  AND extra NOT LIKE '%GENERATED%'
ORDER BY ordinal_position`, schema, table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var columns []string
	for rows.Next() {
		var column string
		if err := rows.Scan(&column); err != nil {
			return nil, err
		}
		columns = append(columns, quoteIdentifier(column))
	}
	return columns, rows.Err()
}

func listTables(adminDB *sql.DB, schema string) ([]string, error) {
	rows, err := adminDB.Query(
		"SELECT table_name FROM information_schema.tables WHERE table_schema = ? AND table_type = 'BASE TABLE' ORDER BY table_name",
		schema,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var tables []string
	for rows.Next() {
		var table string
		if err := rows.Scan(&table); err != nil {
			return nil, err
		}
		tables = append(tables, table)
	}
	return tables, rows.Err()
}

func execSQL(db *sql.DB, query string) error {
	_, err := db.Exec(query)
	return err
}

func quoteIdentifier(name string) string {
	return "`" + strings.ReplaceAll(name, "`", "``") + "`"
}
