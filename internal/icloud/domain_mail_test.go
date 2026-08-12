package icloud

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	governancedomain "github.com/donnel666/remail/internal/governance/domain"
	"github.com/donnel666/remail/internal/mailbox"
	"github.com/donnel666/remail/internal/systemsettings/runtimeconfig"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

type iCloudDomainAliasTestModel struct {
	ID              uint   `gorm:"column:id;primaryKey"`
	ResourceID      uint   `gorm:"column:resource_id"`
	AnonymousID     string `gorm:"column:anonymous_id"`
	Email           string `gorm:"column:email"`
	RecipientMailID string `gorm:"column:recipient_mail_id"`
	ForwardToEmail  string `gorm:"column:forward_to_email"`
	Status          string `gorm:"column:status"`
}

func (iCloudDomainAliasTestModel) TableName() string { return "icloud_aliases" }

type iCloudDomainAllocationTestModel struct {
	ID         uint      `gorm:"column:id;primaryKey"`
	OrderNo    string    `gorm:"column:order_no"`
	ResourceID uint      `gorm:"column:resource_id"`
	AliasID    uint      `gorm:"column:alias_id"`
	Email      string    `gorm:"column:email"`
	Status     string    `gorm:"column:status"`
	CreatedAt  time.Time `gorm:"column:created_at"`
}

func (iCloudDomainAllocationTestModel) TableName() string { return "icloud_allocations" }

type iCloudInboundMailTestModel struct {
	ID              uint      `gorm:"column:id;primaryKey"`
	EnvelopeFrom    string    `gorm:"column:envelope_from"`
	Recipient       string    `gorm:"column:recipient"`
	MailboxKey      string    `gorm:"column:mailbox_key"`
	ResourceType    string    `gorm:"column:resource_type"`
	SourceObjectKey string    `gorm:"column:source_object_key"`
	Status          string    `gorm:"column:status"`
	CreatedAt       time.Time `gorm:"column:created_at"`
}

func (iCloudInboundMailTestModel) TableName() string { return "inbound_mails" }

type iCloudMailIngestCall struct {
	ResourceID        uint
	Recipient         string
	EnvelopeFrom      string
	Raw               []byte
	ReceivedAt        time.Time
	ProviderMessageID string
}

type iCloudMailIngestSpy struct {
	calls []iCloudMailIngestCall
}

func (s *iCloudMailIngestSpy) IngestICloudMail(
	_ context.Context,
	resourceID uint,
	recipient string,
	envelopeFrom string,
	raw []byte,
	receivedAt time.Time,
	providerMessageID string,
) error {
	s.calls = append(s.calls, iCloudMailIngestCall{
		ResourceID: resourceID, Recipient: recipient, EnvelopeFrom: envelopeFrom,
		Raw: append([]byte(nil), raw...), ReceivedAt: receivedAt, ProviderMessageID: providerMessageID,
	})
	return nil
}

func TestDecodeICloudRelaySenderMatchesAnonymousID(t *testing.T) {
	for _, test := range []struct{ envelope, anonymousID, sender string }{
		{"18005575_at_qq_com_zvzv72255gjx92_552k9812@icloud.com", "zvzv5gjx2k9812", "18005575@qq.com"},
		{"donnel.lu_at_foxmail_com_c38c2fr7h7q8pa_n2cs5734@icloud.com", "c82rhqpncs5734", "donnel.lu@foxmail.com"},
		{"donnel.lu_at_foxmail_com_w8p7d2f9mz6zb9_478g0829@icloud.com", "w8p7mz6z8g0829", "donnel.lu@foxmail.com"},
	} {
		sender, ok := decodeICloudRelaySender(test.envelope, test.anonymousID)
		if !ok || sender != test.sender {
			t.Fatalf("decode %q with %q = %q, %v", test.envelope, test.anonymousID, sender, ok)
		}
	}
	if _, ok := decodeICloudRelaySender("18005575_at_qq_com_zvzv72255gjx92_552k9812@icloud.com", "other552k9812"); ok {
		t.Fatal("a different anonymous ID must not select the message")
	}
}

func TestFetchICloudMailUsesPersistedAliasRouteAndNewestReadLimit(t *testing.T) {
	db := newICloudDomainMailTestDB(t, "icloud-domain-order")
	setICloudDomainMailSetting(t, runtimeconfig.ICloudForwardingMailboxesKey, "new-mailbox@aishop6.com")
	setICloudDomainMailSetting(t, "purchase_read_limit", "2")
	setICloudDomainMailSetting(t, "read_window_skew_minutes", "2")

	allocatedAt := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	alias := iCloudDomainAliasTestModel{
		ID: 5, ResourceID: 41, Email: "quietus-route3k@icloud.com",
		AnonymousID: "zvzv5gjx2k9812", ForwardToEmail: "icloud@aishop6.com", Status: "normal",
	}
	if err := db.Create(&alias).Error; err != nil {
		t.Fatalf("create alias: %v", err)
	}
	if err := db.Create(&iCloudDomainAllocationTestModel{
		ID: 1, OrderNo: "ICLOUD-ORDER", ResourceID: alias.ResourceID, AliasID: alias.ID,
		Email: alias.Email, Status: "allocated", CreatedAt: allocatedAt,
	}).Error; err != nil {
		t.Fatalf("create allocation: %v", err)
	}

	files := &icloudImportFileStore{files: make(map[string]governancedomain.PrivateFile)}
	rows := []struct {
		id        uint
		from      string
		recipient string
		createdAt time.Time
	}{
		{1, "too_old_at_example_com_zvzv72255gjx92_552k9812@icloud.com", alias.ForwardToEmail, allocatedAt.Add(-3 * time.Minute)},
		{2, "first_at_example_com_zvzv72255gjx92_552k9812@icloud.com", alias.ForwardToEmail, allocatedAt.Add(-time.Minute)},
		{3, "second_at_example_com_zvzv72255gjx92_552k9812@icloud.com", alias.ForwardToEmail, allocatedAt.Add(time.Minute)},
		{4, "third_at_example_com_zvzv72255gjx92_552k9812@icloud.com", alias.ForwardToEmail, allocatedAt.Add(2 * time.Minute)},
		{5, "wrong_at_example_com_other_552k9812@icloud.com", alias.ForwardToEmail, allocatedAt.Add(3 * time.Minute)},
		{6, "wrong_mailbox_at_example_com_zvzv72255gjx92_552k9812@icloud.com", "other@aishop6.com", allocatedAt.Add(4 * time.Minute)},
	}
	for _, row := range rows {
		storeICloudInboundMail(t, db, files, iCloudInboundMailTestModel{
			ID: row.id, EnvelopeFrom: row.from, Recipient: row.recipient, ResourceType: "domain",
			SourceObjectKey: fmt.Sprintf("inbound/%d.eml", row.id), Status: "stored", CreatedAt: row.createdAt,
		})
	}

	ingest := &iCloudMailIngestSpy{}
	service := NewService(db, nil, files)
	service.SetMailIngest(ingest)
	if err := service.FetchICloudMail(context.Background(), "ICLOUD-ORDER"); err != nil {
		t.Fatalf("fetch iCloud order mail: %v", err)
	}
	if len(ingest.calls) != 2 {
		t.Fatalf("ingested calls = %#v", ingest.calls)
	}
	if ingest.calls[0].EnvelopeFrom != "second@example.com" || ingest.calls[1].EnvelopeFrom != "third@example.com" ||
		ingest.calls[0].Recipient != alias.Email || ingest.calls[1].Recipient != alias.Email ||
		ingest.calls[0].ReceivedAt.After(ingest.calls[1].ReceivedAt) {
		t.Fatalf("unexpected normalized calls: %#v", ingest.calls)
	}
	for _, call := range ingest.calls {
		if call.ResourceID != alias.ResourceID || strings.Contains(call.ProviderMessageID, alias.AnonymousID) ||
			strings.Contains(call.ProviderMessageID, alias.ForwardToEmail) {
			t.Fatalf("internal routing fact reached Mailmatch fields: %#v", call)
		}
	}
}

func TestFetchICloudResourceMailReadsAllPersistedAliases(t *testing.T) {
	db := newICloudDomainMailTestDB(t, "icloud-domain-admin")
	setICloudDomainMailSetting(t, "purchase_read_limit", "30")
	allocatedAt := time.Date(2026, 8, 9, 13, 0, 0, 0, time.UTC)
	aliases := []iCloudDomainAliasTestModel{
		{ID: 5, ResourceID: 41, AnonymousID: "recipientfirst", Email: "first@icloud.com", ForwardToEmail: "icloud@aishop6.com", Status: "normal"},
		{ID: 6, ResourceID: 41, AnonymousID: "recipientsecond", Email: "second@icloud.com", ForwardToEmail: "archive@aishop6.com", Status: "normal"},
		{ID: 7, ResourceID: 41, AnonymousID: "recipientreleased", Email: "released@icloud.com", ForwardToEmail: "icloud@aishop6.com", Status: "released"},
	}
	if err := db.Create(&aliases).Error; err != nil {
		t.Fatalf("create aliases: %v", err)
	}
	allocations := []iCloudDomainAllocationTestModel{
		{ID: 1, OrderNo: "FIRST", ResourceID: 41, AliasID: 5, Email: aliases[0].Email, Status: "allocated", CreatedAt: allocatedAt},
		{ID: 2, OrderNo: "SECOND", ResourceID: 41, AliasID: 6, Email: aliases[1].Email, Status: "allocated", CreatedAt: allocatedAt.Add(time.Minute)},
		{ID: 3, OrderNo: "RELEASED", ResourceID: 41, AliasID: 7, Email: aliases[2].Email, Status: "released", CreatedAt: allocatedAt.Add(2 * time.Minute)},
	}
	if err := db.Create(&allocations).Error; err != nil {
		t.Fatalf("create allocations: %v", err)
	}

	files := &icloudImportFileStore{files: make(map[string]governancedomain.PrivateFile)}
	for index, alias := range aliases {
		storeICloudInboundMail(t, db, files, iCloudInboundMailTestModel{
			ID: uint(index + 1), EnvelopeFrom: fmt.Sprintf("sender%d_at_example_com_recipient_%s@icloud.com", index+1, []string{"first", "second", "released"}[index]),
			Recipient: alias.ForwardToEmail, ResourceType: "domain", SourceObjectKey: fmt.Sprintf("admin/%d.eml", index+1),
			Status: "stored", CreatedAt: allocatedAt.Add(time.Duration(index+1) * time.Minute),
		})
	}

	ingest := &iCloudMailIngestSpy{}
	service := NewService(db, nil, files)
	service.SetMailIngest(ingest)
	if err := service.FetchICloudResourceMail(context.Background(), 41); err != nil {
		t.Fatalf("fetch administrator iCloud mail: %v", err)
	}
	if len(ingest.calls) != 3 || ingest.calls[0].Recipient != aliases[0].Email || ingest.calls[1].Recipient != aliases[1].Email || ingest.calls[2].Recipient != aliases[2].Email ||
		ingest.calls[0].EnvelopeFrom != "sender1@example.com" || ingest.calls[1].EnvelopeFrom != "sender2@example.com" || ingest.calls[2].EnvelopeFrom != "sender3@example.com" {
		t.Fatalf("administrator calls = %#v", ingest.calls)
	}
}

func TestFetchICloudResourceMailReadsHistoricalRoutesForAdministrator(t *testing.T) {
	db := newICloudDomainMailTestDB(t, "icloud-domain-admin-history")
	setICloudDomainMailSetting(t, runtimeconfig.ICloudAdminReadLimitKey, "10")
	now := time.Date(2026, 8, 9, 15, 0, 0, 0, time.UTC)
	alias := iCloudDomainAliasTestModel{ID: 5, ResourceID: 41, AnonymousID: "recipient", Email: "history@icloud.com", ForwardToEmail: "new@aishop6.com", Status: "normal"}
	if err := db.Create(&alias).Error; err != nil {
		t.Fatalf("create alias: %v", err)
	}
	if err := db.Create(&iCloudAliasRouteModel{ResourceID: 41, AliasID: 5, ForwardToEmail: "old@aishop6.com", RecipientMailID: "old-recipient", FirstSeenAt: now.Add(time.Hour), LastSeenAt: now.Add(2 * time.Hour)}).Error; err != nil {
		t.Fatalf("create old route: %v", err)
	}
	if err := db.Create(&iCloudAliasRouteModel{ResourceID: 41, AliasID: 5, ForwardToEmail: "new@aishop6.com", RecipientMailID: "new-recipient", FirstSeenAt: now.Add(-time.Minute), LastSeenAt: now}).Error; err != nil {
		t.Fatalf("create current route: %v", err)
	}
	files := &icloudImportFileStore{files: make(map[string]governancedomain.PrivateFile)}
	for id, row := range []struct {
		from, recipient, key string
	}{
		{"old_sender_at_example_com_old-recipient@icloud.com", "old@aishop6.com", "history/old.eml"},
		{"new_sender_at_example_com_new-recipient@icloud.com", "new@aishop6.com", "history/new.eml"},
	} {
		storeICloudInboundMail(t, db, files, iCloudInboundMailTestModel{ID: uint(id + 1), EnvelopeFrom: row.from, Recipient: row.recipient, ResourceType: "domain", SourceObjectKey: row.key, Status: "stored", CreatedAt: now.Add(time.Duration(id) * time.Minute)})
	}
	ingest := &iCloudMailIngestSpy{}
	service := NewService(db, nil, files)
	service.SetMailIngest(ingest)
	if err := service.FetchICloudResourceMail(context.Background(), 41); err != nil {
		t.Fatalf("fetch administrator history: %v", err)
	}
	if len(ingest.calls) != 2 || ingest.calls[0].EnvelopeFrom != "old.sender@example.com" || ingest.calls[1].EnvelopeFrom != "new.sender@example.com" {
		t.Fatalf("administrator must read current and historical routes: %#v", ingest.calls)
	}
}

func TestFetchICloudResourceMailAppliesReadLimitAfterGlobalNewestSort(t *testing.T) {
	db := newICloudDomainMailTestDB(t, "icloud-domain-admin-limit")
	setICloudDomainMailSetting(t, "purchase_read_limit", "1")
	setICloudDomainMailSetting(t, runtimeconfig.ICloudAdminReadLimitKey, "2")
	base := time.Date(2026, 8, 9, 16, 0, 0, 0, time.UTC)
	aliases := []iCloudDomainAliasTestModel{
		{ID: 5, ResourceID: 41, AnonymousID: "recipientfirst", Email: "first@icloud.com", ForwardToEmail: "icloud@aishop6.com", Status: "normal"},
		{ID: 6, ResourceID: 41, AnonymousID: "recipientsecond", Email: "second@icloud.com", ForwardToEmail: "icloud@aishop6.com", Status: "normal"},
	}
	if err := db.Create(&aliases).Error; err != nil {
		t.Fatalf("create aliases: %v", err)
	}
	files := &icloudImportFileStore{files: make(map[string]governancedomain.PrivateFile)}
	rows := []iCloudInboundMailTestModel{
		{ID: 1, EnvelopeFrom: "oldest_at_example_com_recipient-first@icloud.com", Recipient: "icloud@aishop6.com", ResourceType: "domain", SourceObjectKey: "limit/oldest.eml", Status: "stored", CreatedAt: base},
		{ID: 2, EnvelopeFrom: "second_at_example_com_recipient-first@icloud.com", Recipient: "icloud@aishop6.com", ResourceType: "domain", SourceObjectKey: "limit/second.eml", Status: "stored", CreatedAt: base.Add(time.Minute)},
		{ID: 3, EnvelopeFrom: "newest_at_example_com_recipient-second@icloud.com", Recipient: "icloud@aishop6.com", ResourceType: "domain", SourceObjectKey: "limit/newest.eml", Status: "stored", CreatedAt: base.Add(2 * time.Minute)},
	}
	for _, row := range rows {
		storeICloudInboundMail(t, db, files, row)
	}
	ingest := &iCloudMailIngestSpy{}
	service := NewService(db, nil, files)
	service.SetMailIngest(ingest)
	if err := service.FetchICloudResourceMail(context.Background(), 41); err != nil {
		t.Fatalf("fetch administrator limit: %v", err)
	}
	if len(ingest.calls) != 2 || ingest.calls[0].EnvelopeFrom != "second@example.com" || ingest.calls[1].EnvelopeFrom != "newest@example.com" ||
		ingest.calls[0].Recipient != aliases[0].Email || ingest.calls[1].Recipient != aliases[1].Email {
		t.Fatalf("administrator limit must keep the globally newest mail: %#v", ingest.calls)
	}
}

func TestFetchICloudResourceMailRejectsAmbiguousAnonymousID(t *testing.T) {
	db := newICloudDomainMailTestDB(t, "icloud-domain-recipient-id")
	aliases := []iCloudDomainAliasTestModel{
		{ID: 5, ResourceID: 41, AnonymousID: "recipient", Email: "short@icloud.com", ForwardToEmail: "icloud@aishop6.com", Status: "normal"},
		{ID: 6, ResourceID: 41, AnonymousID: "longrecipient", Email: "long@icloud.com", ForwardToEmail: "icloud@aishop6.com", Status: "normal"},
	}
	if err := db.Create(&aliases).Error; err != nil {
		t.Fatalf("create aliases: %v", err)
	}
	files := &icloudImportFileStore{files: make(map[string]governancedomain.PrivateFile)}
	storeICloudInboundMail(t, db, files, iCloudInboundMailTestModel{
		ID: 1, EnvelopeFrom: "sender_at_example_com_long_recipient@icloud.com", Recipient: "icloud@aishop6.com",
		ResourceType: "domain", SourceObjectKey: "recipient-id/1.eml", Status: "stored", CreatedAt: time.Date(2026, 8, 9, 17, 30, 0, 0, time.UTC),
	})
	ingest := &iCloudMailIngestSpy{}
	service := NewService(db, nil, files)
	service.SetMailIngest(ingest)
	if err := service.FetchICloudResourceMail(context.Background(), 41); err != nil {
		t.Fatalf("fetch administrator mail: %v", err)
	}
	if len(ingest.calls) != 0 {
		t.Fatalf("ambiguous anonymous IDs must not select an alias: %#v", ingest.calls)
	}
}

func TestFetchICloudMailStopsAtConfiguredRecentMailboxWindow(t *testing.T) {
	db := newICloudDomainMailTestDB(t, "icloud-domain-scan-window")
	setICloudDomainMailSetting(t, runtimeconfig.ICloudForwardingMailboxesKey, "ICLOUD@AISHOP6.COM")
	setICloudDomainMailSetting(t, runtimeconfig.ICloudMailmatchScanLimitKey, "3")
	setICloudDomainMailSetting(t, "purchase_read_limit", "2")
	base := time.Date(2026, 8, 9, 17, 0, 0, 0, time.UTC)
	alias := iCloudDomainAliasTestModel{
		ID: 5, ResourceID: 41, AnonymousID: "recipientscan", Email: "scan@icloud.com", ForwardToEmail: "icloud@aishop6.com", Status: "normal",
	}
	if err := db.Create(&alias).Error; err != nil {
		t.Fatalf("create alias: %v", err)
	}
	if err := db.Create(&iCloudDomainAllocationTestModel{
		ID: 1, OrderNo: "SCAN-WINDOW", ResourceID: alias.ResourceID, AliasID: alias.ID,
		Email: alias.Email, Status: "allocated", CreatedAt: base,
	}).Error; err != nil {
		t.Fatalf("create allocation: %v", err)
	}
	files := &icloudImportFileStore{files: make(map[string]governancedomain.PrivateFile)}
	rows := []iCloudInboundMailTestModel{
		{ID: 1, EnvelopeFrom: "newest_at_example_com_wrong@icloud.com", Recipient: "ICLOUD@AISHOP6.COM", ResourceType: "domain", SourceObjectKey: "scan/1.eml", Status: "stored", CreatedAt: base.Add(3 * time.Minute)},
		{ID: 2, EnvelopeFrom: "newer_at_example_com_other@icloud.com", Recipient: "icloud@aishop6.com", ResourceType: "domain", SourceObjectKey: "scan/2.eml", Status: "stored", CreatedAt: base.Add(2 * time.Minute)},
		{ID: 3, EnvelopeFrom: "new_at_example_com_recipient-scan@icloud.com", Recipient: "icloud@aishop6.com", ResourceType: "domain", SourceObjectKey: "scan/3.eml", Status: "stored", CreatedAt: base.Add(time.Minute)},
		{ID: 4, EnvelopeFrom: "old_at_example_com_recipient-scan@icloud.com", Recipient: "icloud@aishop6.com", ResourceType: "domain", SourceObjectKey: "scan/4.eml", Status: "stored", CreatedAt: base},
	}
	for _, row := range rows {
		storeICloudInboundMail(t, db, files, row)
	}
	// The SMTP envelope may retain arbitrary case; mailbox_key is the routing key.
	ingest := &iCloudMailIngestSpy{}
	service := NewService(db, nil, files)
	service.SetMailIngest(ingest)
	if err := service.FetchICloudMail(context.Background(), "SCAN-WINDOW"); err != nil {
		t.Fatalf("fetch iCloud order mail: %v", err)
	}
	if len(ingest.calls) != 1 || ingest.calls[0].EnvelopeFrom != "new@example.com" {
		t.Fatalf("must stop after the configured recent window: %#v", ingest.calls)
	}
}

func newICloudDomainMailTestDB(t *testing.T, name string) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+name+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	if err := db.AutoMigrate(
		&iCloudDomainAliasTestModel{},
		&iCloudDomainAllocationTestModel{},
		&iCloudInboundMailTestModel{},
		&iCloudAliasRouteModel{},
	); err != nil {
		t.Fatalf("migrate database: %v", err)
	}
	return db
}

func storeICloudInboundMail(t *testing.T, db *gorm.DB, files *icloudImportFileStore, row iCloudInboundMailTestModel) {
	t.Helper()
	if row.MailboxKey == "" {
		row.MailboxKey = mailbox.Normalize(row.Recipient)
	}
	if err := db.Create(&row).Error; err != nil {
		t.Fatalf("create inbound mail: %v", err)
	}
	if _, err := files.SavePrivate(context.Background(), governancedomain.PrivateFile{
		ObjectKey:    row.SourceObjectKey,
		ContentBytes: []byte("From: forwarded@example.invalid\r\nTo: " + row.Recipient + "\r\nSubject: test\r\n\r\nbody"),
	}); err != nil {
		t.Fatalf("store inbound object: %v", err)
	}
}

func setICloudDomainMailSetting(t *testing.T, key string, value string) {
	t.Helper()
	previous := runtimeconfig.Snapshot()
	previousValue, existed := previous[key]
	runtimeconfig.Set(key, value)
	t.Cleanup(func() {
		if existed {
			runtimeconfig.Set(key, previousValue)
		} else {
			runtimeconfig.Delete(key)
		}
	})
}
