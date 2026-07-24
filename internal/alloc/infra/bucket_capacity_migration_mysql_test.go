package infra

import (
	"testing"

	coredomain "github.com/donnel666/remail/internal/core/domain"
	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestAllocationBucketCapacityMigrationRoundTripMySQL(t *testing.T) {
	db := newAllocMySQLTestDB(t)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	require.NoError(t, goose.SetDialect("mysql"))
	require.NoError(t, goose.DownTo(sqlDB, allocMigrationsDir(t), 45))

	seedAllocBase(t, db, "microsoft", 1, 0, 0)
	require.NoError(t, db.Exec(`
INSERT INTO email_resources(id, type, owner_user_id) VALUES
    (2047, 'microsoft', 1),
    (4095, 'domain', 1)`).Error)
	require.NoError(t, db.Exec(`
INSERT INTO microsoft_resources(
    id, email_address, email_domain, password, for_sale, status, quality_score, alloc_bucket
) VALUES (2047, 'ms2047@example.com', 'example.com', 'secret', TRUE, 'normal', 100, MOD(2047, 64))`).Error)
	require.NoError(t, db.Exec(`
INSERT INTO mail_servers(id, owner_user_id, name, server_address, mx_record, status)
VALUES (990460, 1, 'bucket-migration', 'mx.bucket.test', 'mx.bucket.test', 'online')`).Error)
	require.NoError(t, db.Exec(`
INSERT INTO domain_resources(
    id, resource_type, owner_user_id, domain, domain_tld, mail_server_id, purpose, status, alloc_bucket
) VALUES (4095, 'domain', 1, 'd4095.example.com', 'example.com', 990460, 'sale', 'normal', MOD(4095, 64))`).Error)
	require.NoError(t, db.Exec(`
INSERT INTO generated_mailboxes(id, resource_id, owner_user_id, email, status)
VALUES (2047, 4095, 1, ' Existing@D4095.Example.com ', 'normal')`).Error)
	require.NoError(t, db.Exec(`
INSERT INTO microsoft_routing_candidates(
    id, project_id, resource_id, email_address, domain_suffix, for_sale, quality_score, status, alloc_bucket
) VALUES (990461, 10, 2047, 'ms2047@example.com', 'example.com', TRUE, 100, 'normal', MOD(2047, 64))`).Error)
	require.NoError(t, db.Exec(`
INSERT INTO domain_routing_candidates(
    id, project_id, resource_id, domain, domain_tld, purpose, status, alloc_bucket
) VALUES (990462, 10, 4095, 'd4095.example.com', 'example.com', 'sale', 'normal', MOD(4095, 64))`).Error)

	require.NoError(t, goose.UpTo(sqlDB, allocMigrationsDir(t), 46))
	assertAllocationBuckets(t, db, 2047, 511)
	requireIndexExists(t, db, "generated_mailboxes", "idx_generated_mailboxes_alloc_reuse")
	requireIndexExists(t, db, "generated_mailboxes", "idx_generated_mailboxes_bucket_reuse")
	requireIndexMissing(t, db, "generated_mailboxes", "idx_generated_mailboxes_resource_bucket_reuse")
	var generatedBucket uint16
	require.NoError(t, db.Raw("SELECT alloc_bucket FROM generated_mailboxes WHERE id = 2047").Scan(&generatedBucket).Error)
	require.Equal(t, coredomain.GeneratedMailboxBucket("existing@d4095.example.com"), generatedBucket)

	require.NoError(t, goose.DownTo(sqlDB, allocMigrationsDir(t), 45))
	assertAllocationBuckets(t, db, 63, 63)
	requireIndexExists(t, db, "generated_mailboxes", "idx_generated_mailboxes_alloc_reuse")
	requireIndexMissing(t, db, "generated_mailboxes", "idx_generated_mailboxes_bucket_reuse")
	var generatedBucketColumns int64
	require.NoError(t, db.Raw(`
SELECT COUNT(*)
FROM information_schema.columns
WHERE table_schema = DATABASE()
  AND table_name = 'generated_mailboxes'
  AND column_name = 'alloc_bucket'`).Scan(&generatedBucketColumns).Error)
	require.Zero(t, generatedBucketColumns)
}

func assertAllocationBuckets(t *testing.T, db *gorm.DB, microsoftWant uint16, domainWant uint16) {
	t.Helper()
	for _, test := range []struct {
		query string
		want  uint16
	}{
		{query: "SELECT alloc_bucket FROM microsoft_resources WHERE id = 2047", want: microsoftWant},
		{query: "SELECT alloc_bucket FROM microsoft_routing_candidates WHERE resource_id = 2047", want: microsoftWant},
		{query: "SELECT alloc_bucket FROM domain_resources WHERE id = 4095", want: domainWant},
		{query: "SELECT alloc_bucket FROM domain_routing_candidates WHERE resource_id = 4095", want: domainWant},
	} {
		var got uint16
		require.NoError(t, db.Raw(test.query).Scan(&got).Error)
		require.Equal(t, test.want, got, test.query)
	}
}
