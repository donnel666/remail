package infra

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/donnel666/remail/internal/proto/domain"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

func TestProtoValidationFailureFacetsMySQL(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	server, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image: "mysql:8.0", ExposedPorts: []string{"3306/tcp"},
			Env:        map[string]string{"MYSQL_ROOT_PASSWORD": "fixture", "MYSQL_DATABASE": "proto_projection"},
			Tmpfs:      map[string]string{"/var/lib/mysql": "rw,size=512m"},
			Cmd:        []string{"--skip-log-bin", "--mysqlx=OFF", "--innodb-buffer-pool-size=64M", "--innodb-redo-log-capacity=64M"},
			WaitingFor: wait.ForLog("ready for connections").WithOccurrence(2).WithStartupTimeout(time.Minute),
		},
		Started: true,
	})
	if server != nil {
		t.Cleanup(func() {
			cleanup, stop := context.WithTimeout(context.Background(), 20*time.Second)
			defer stop()
			require.NoError(t, server.Terminate(cleanup))
		})
	}
	require.NoError(t, err)
	host, err := server.Host(ctx)
	require.NoError(t, err)
	port, err := server.MappedPort(ctx, "3306/tcp")
	require.NoError(t, err)
	db, err := gorm.Open(mysql.New(mysql.Config{
		DSN:               fmt.Sprintf("root:fixture@tcp(%s:%s)/proto_projection?parseTime=true&timeout=5s&readTimeout=5s&writeTimeout=5s", host, port.Port()),
		DefaultStringSize: 320,
	}), &gorm.Config{})
	require.NoError(t, err)
	pool, err := db.DB()
	require.NoError(t, err)
	pool.SetMaxOpenConns(1)
	t.Cleanup(func() { require.NoError(t, pool.Close()) })
	var mode string
	require.NoError(t, db.Raw("SELECT @@SESSION.sql_mode").Scan(&mode).Error)
	require.Contains(t, mode, "ONLY_FULL_GROUP_BY")
	// Only the three query fixtures are needed; do not run application migrations.
	require.NoError(t, db.AutoMigrate(&resourceRoot{}, &Resource{}, &MaintenanceRun{}))
	now := time.Now().UTC()
	for i, status := range []string{domain.StatusPending, domain.StatusPending, domain.StatusPending, domain.StatusNormal} {
		id := uint(i + 1)
		require.NoError(t, db.Create(&resourceRoot{ID: id, Type: domain.ResourceType, OwnerUserID: 7, Version: 1}).Error)
		require.NoError(t, db.Create(&Resource{ID: id, ResourceType: domain.ResourceType, OwnerUserID: 7,
			EmailAddress: fmt.Sprintf("projection%d@proton.me", id), EmailDomain: "proton.me", Status: status,
			ValidationGeneration: 1, CredentialRevision: 1, CredentialUpdatedAt: now}).Error)
	}
	for i, outcome := range []string{maintenanceFailed, maintenanceUncertain} {
		require.NoError(t, db.Create(&MaintenanceRun{ResourceID: uint(i + 2), Kind: maintenanceKindValidation,
			Status: outcome, ValidationGeneration: 1, CredentialRevision: 1, QueuedAt: now}).Error)
	}
	s := NewService(db)
	for _, test := range []struct {
		status string
		total  int64
	}{{"", 4}, {domain.StatusPending, 1}, {domain.StatusValidationFailed, 2}} {
		page, err := s.ListResources(ctx, ResourceFilter{Status: test.status, Limit: 10})
		require.NoError(t, err, test.status)
		require.Equal(t, test.total, page.Total)
		require.Len(t, page.Items, int(test.total))
		require.EqualValues(t, 1, page.Facets.Status.Pending)
		require.EqualValues(t, 2, page.Facets.Status.ValidationFailed)
		require.EqualValues(t, 1, page.Facets.Status.Normal)
	}
}
