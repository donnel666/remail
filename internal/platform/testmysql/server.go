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
	prefix    string
	once      sync.Once
	container testcontainers.Container
	host      string
	port      string
	err       error
	nextDB    uint64
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

	dbName := fmt.Sprintf("%s_%d", s.prefix, atomic.AddUint64(&s.nextDB, 1))
	adminDB, err := sql.Open("mysql", fmt.Sprintf("root:root@tcp(%s:%s)/?charset=utf8mb4&parseTime=True&loc=Local&multiStatements=true", s.host, s.port))
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = adminDB.Exec("DROP DATABASE IF EXISTS " + quoteIdentifier(dbName))
		require.NoError(t, adminDB.Close())
	})

	require.NoError(t, adminDB.Ping())
	require.NoError(t, execSQL(adminDB, "CREATE DATABASE "+quoteIdentifier(dbName)+" CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci"))
	require.NoError(t, execSQL(adminDB, "GRANT ALL PRIVILEGES ON "+quoteIdentifier(dbName)+".* TO 'remail'@'%'"))

	dsn := fmt.Sprintf("remail:remail@tcp(%s:%s)/%s?charset=utf8mb4&parseTime=True&loc=Local", s.host, s.port, dbName)
	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{TranslateError: true})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, sqlDB.Close())
	})

	require.NoError(t, platform.RunMigrations(sqlDB, migrationsDir))
	return db
}

func (s *Server) Close(ctx context.Context) error {
	if s == nil || s.container == nil {
		return nil
	}
	return s.container.Terminate(ctx)
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
		sqlDB, lastErr = sql.Open("mysql", fmt.Sprintf("root:root@tcp(%s:%s)/?charset=utf8mb4&parseTime=True&loc=Local", s.host, s.port))
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

func execSQL(db *sql.DB, query string) error {
	_, err := db.Exec(query)
	return err
}

func quoteIdentifier(name string) string {
	return "`" + strings.ReplaceAll(name, "`", "``") + "`"
}
