package infra

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/donnel666/remail/internal/proto/domain"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

func TestProtoValidationTerminalMigrationAndFacetsMySQL(t *testing.T) {
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
	require.NoError(t, db.AutoMigrate(&resourceRoot{}, &Resource{}, &MaintenanceRun{}))
	now := time.Now().UTC()
	fixtures := []struct {
		status, kind, outcome, want string
		generation, revision        uint64
	}{
		{domain.StatusPending, "", "", domain.StatusPending, 1, 1},
		{domain.StatusPending, maintenanceKindValidation, maintenanceFailed, domain.StatusAbnormal, 1, 1},
		{domain.StatusPending, maintenanceKindValidation, maintenanceUncertain, domain.StatusAbnormal, 1, 1},
		{domain.StatusNormal, maintenanceKindValidation, maintenanceFailed, domain.StatusNormal, 1, 1},
		{domain.StatusPending, maintenanceKindValidation, maintenanceFailed, domain.StatusPending, 2, 1},
		{domain.StatusPending, maintenanceKindValidation, maintenanceFailed, domain.StatusPending, 1, 2},
		{domain.StatusPending, maintenanceKindHistory, maintenanceFailed, domain.StatusPending, 1, 1},
		{domain.StatusDisabled, maintenanceKindValidation, maintenanceFailed, domain.StatusDisabled, 1, 1},
		{domain.StatusDeleted, maintenanceKindValidation, maintenanceFailed, domain.StatusDeleted, 1, 1},
	}
	for i, fixture := range fixtures {
		id := uint(i + 1)
		require.NoError(t, db.Create(&resourceRoot{ID: id, Type: domain.ResourceType, OwnerUserID: 7, Version: 1}).Error)
		require.NoError(t, db.Create(&Resource{ID: id, ResourceType: domain.ResourceType, OwnerUserID: 7,
			EmailAddress: fmt.Sprintf("migration%d@proton.me", id), EmailDomain: "proton.me", Status: fixture.status, Version: 1,
			ValidationGeneration: fixture.generation, CredentialRevision: fixture.revision, CredentialUpdatedAt: now}).Error)
		if fixture.kind != "" {
			require.NoError(t, db.Create(&MaintenanceRun{ResourceID: id, Kind: fixture.kind,
				Status: fixture.outcome, ValidationGeneration: 1, CredentialRevision: 1, QueuedAt: now}).Error)
		}
	}
	migration, err := os.ReadFile("../../../migrations/00140_proto_validation_terminal_status.sql")
	require.NoError(t, err)
	up := strings.Split(string(migration), "-- +goose Down")[0]
	for range 2 {
		require.NoError(t, db.Exec(up).Error)
		for i, fixture := range fixtures {
			var row Resource
			var root resourceRoot
			require.NoError(t, db.First(&row, i+1).Error)
			require.NoError(t, db.First(&root, i+1).Error)
			require.Equal(t, fixture.want, row.Status)
			wantVersion := uint64(1)
			if fixture.want == domain.StatusAbnormal {
				wantVersion++
			}
			require.Equal(t, wantVersion, row.Version)
			require.Equal(t, wantVersion, root.Version)
			require.Equal(t, fixture.generation, row.ValidationGeneration)
			require.Equal(t, fixture.revision, row.CredentialRevision)
		}
	}
	s := NewService(db)
	for _, test := range []struct {
		status string
		total  int64
	}{{"", 8}, {domain.StatusPending, 4}, {domain.StatusAbnormal, 2}} {
		page, err := s.ListResources(ctx, ResourceFilter{Status: test.status, Limit: 10})
		require.NoError(t, err, test.status)
		require.Equal(t, test.total, page.Total)
		require.Len(t, page.Items, int(test.total))
		require.EqualValues(t, 4, page.Facets.Status.Pending)
		require.EqualValues(t, 2, page.Facets.Status.Abnormal)
		require.EqualValues(t, 1, page.Facets.Status.Normal)
	}
	_, err = s.ListResources(ctx, ResourceFilter{Status: "validation_failed", Limit: 10})
	require.ErrorIs(t, err, domain.ErrInvalidResource)
}
