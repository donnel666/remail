package infra

import (
	"context"
	"errors"
	"testing"

	allocapp "github.com/donnel666/remail/internal/alloc/app"
	"github.com/donnel666/remail/internal/alloc/domain"
	"github.com/donnel666/remail/internal/platform"
	"github.com/donnel666/remail/internal/platform/testmysql"
	"github.com/donnel666/remail/internal/systemsettings/runtimeconfig"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type daumCandidateQueryCounter struct {
	*Repo
	batches [][]uint16
}

func (r *daumCandidateQueryCounter) ListDaumSourceCandidates(ctx context.Context, project, buyer uint, scope domain.SupplyScope, buckets []uint16, limit int, suffix string) ([]allocapp.DaumCandidate, error) {
	r.batches = append(r.batches, append([]uint16(nil), buckets...))
	return r.Repo.ListDaumSourceCandidates(ctx, project, buyer, scope, buckets, limit, suffix)
}

func TestDaumSparseInventoryAllocatesFromAll256HashedStartsWithBoundedQueries(t *testing.T) {
	previous, exists := runtimeconfig.Snapshot()["bucket_probe_count"]
	runtimeconfig.Set("bucket_probe_count", "4")
	t.Cleanup(func() {
		if exists {
			runtimeconfig.Set("bucket_probe_count", previous)
		} else {
			runtimeconfig.Delete("bucket_probe_count")
		}
	})
	db := newDaumInventoryDB(t).Session(&gorm.Session{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, db.Migrator().DropTable(&DaumAllocationModel{}))
	require.NoError(t, db.AutoMigrate(&DaumAllocationModel{}, &OrderGuardModel{}))
	require.NoError(t, db.Exec(`
CREATE TABLE projects(id INTEGER PRIMARY KEY,status TEXT,access_type TEXT);
CREATE TABLE project_accesses(project_id INTEGER,user_id INTEGER);
CREATE TABLE project_products(id INTEGER PRIMARY KEY,project_id INTEGER,type TEXT,status TEXT,code_enabled BOOLEAN,purchase_enabled BOOLEAN,code_supplier_price TEXT,purchase_supplier_price TEXT,main_weight INTEGER,dot_weight INTEGER,plus_weight INTEGER);
INSERT INTO projects VALUES(4,'listed','public');
INSERT INTO project_products VALUES(5,4,'daum','enabled',1,1,'0.25','0.50',1,0,0);
`).Error)
	ctx := context.Background()
	reset := errors.New("rollback to reuse the single inventory fixture")
	maximumQueries := 0
	for start := uint16(0); start < allocapp.DaumBucketCount; start++ {
		repo := &daumCandidateQueryCounter{Repo: NewRepo(db)}
		allocator := allocapp.NewUseCase(repo)
		allocator.SetDaumProtocolReady(true)
		orderNo := testmysql.AllocationOrderNo("daum-sparse", 4, "main", start, allocapp.DaumBucketCount)
		err := db.Transaction(func(tx *gorm.DB) error {
			allocation, err := allocator.Allocate(platform.WithGormTx(ctx, tx), allocapp.AllocateCommand{OrderNo: orderNo, BuyerUserID: 2, ProjectProductID: 5, SupplyScope: domain.SupplyScopePublic, EmailSuffix: "daum.net", ServiceMode: domain.ServiceModeCode})
			require.NoError(t, err, "hash start %d must find the sole public account in bucket 10", start)
			require.Equal(t, "public@daum.net", allocation.Email)
			return reset
		})
		require.ErrorIs(t, err, reset)
		require.LessOrEqual(t, len(repo.batches), 7, "each candidate batch executes one bounded SQL query")
		require.Equal(t, []uint16{start}, repo.batches[0])
		seen := map[uint16]bool{}
		for _, batch := range repo.batches {
			require.NotEmpty(t, batch)
			require.LessOrEqual(t, len(batch), 100)
			for _, bucket := range batch {
				require.False(t, seen[bucket], "bucket %d was queried twice for hash start %d", bucket, start)
				seen[bucket] = true
			}
		}
		maximumQueries = max(maximumQueries, len(repo.batches))
	}
	require.Equal(t, 7, maximumQueries, "the last partial batch must also reach sparse inventory")
}
