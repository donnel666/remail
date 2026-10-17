package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/donnel666/remail/api/middleware"
	"github.com/donnel666/remail/internal/daum/domain"
	"github.com/donnel666/remail/internal/daum/infra"
	governanceapi "github.com/donnel666/remail/internal/governance/api"
	governanceapp "github.com/donnel666/remail/internal/governance/app"
	iamdomain "github.com/donnel666/remail/internal/iam/domain"
	mailmatchinfra "github.com/donnel666/remail/internal/mailmatch/infra"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestDaumDetailReadsOwnTasksThroughSharedGovernanceAdapter(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	pool, err := db.DB()
	require.NoError(t, err)
	pool.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = pool.Close() })
	require.NoError(t, db.AutoMigrate(&testRoot{}, &infra.Resource{}, &infra.ExplicitAlias{}, &infra.BindingMailbox{}, &infra.MaintenanceRun{}, &infra.ImportStatus{}))
	require.NoError(t, db.Table("mailmatch_admin_resource_fetch_states").AutoMigrate(&mailmatchinfra.FetchStateModel{}))
	require.NoError(t, db.Exec(`CREATE TABLE daum_resource_import_items(import_id INTEGER,resource_id INTEGER,outcome TEXT,category TEXT);
CREATE TABLE microsoft_resources(id INTEGER PRIMARY KEY);`).Error)
	service := infra.NewService(db)
	id, _, err := service.ImportLine(context.Background(), 7, domain.ImportLine{Email: "detail@daum.net", AppPassword: "synthetic-app"})
	require.NoError(t, err)
	now := time.Now().UTC().Truncate(time.Second)
	run := infra.MaintenanceRun{ResourceID: id, Kind: "validation", Status: "failed", Attempts: 1, MaxAttempts: 3, CredentialRevision: 1, ValidationGeneration: 1, QueuedAt: now, UpdatedAt: now}
	require.NoError(t, db.Create(&run).Error)
	governance := governanceapi.NewModule(db)
	module := NewModuleWithQueue(service, nil)
	module.Tasks = governanceapi.NewCoreTaskQueryAdapter(governance.Tasks, governanceapp.AdminTaskBizDaumResource)
	router := gin.New()
	RegisterRoutes(router.Group("/v1"), module, middleware.SessionFetcherFunc(func(context.Context, string) (uint, iamdomain.Role, string, bool) {
		return 1, iamdomain.RoleAdmin, "admin@example.test", true
	}), allowDaumPermission{})
	request := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/v1/admin/daum/resources/%d", id), nil)
	request.AddCookie(&http.Cookie{Name: middleware.SessionCookieName, Value: "session"})
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	var body daumResourceResponse
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &body))
	require.Len(t, body.RecentTasks, 1)
	require.Equal(t, fmt.Sprintf("daum_validation:%d", run.ID), body.RecentTasks[0].TaskID)
	require.Equal(t, "failed", body.RecentTasks[0].Status)
	require.Nil(t, body.ActiveTask)
	require.NotContains(t, response.Body.String(), "synthetic-app")
	_, err = governance.CoreTaskQuery.GetRecentByResourceID(context.Background(), id, 5)
	require.ErrorIs(t, err, governanceapp.ErrAdminTaskResourceGone, "the Microsoft adapter must retain its resource scope")
}
