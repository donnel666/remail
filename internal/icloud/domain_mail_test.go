package icloud

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"testing"
	"time"

	governancedomain "github.com/donnel666/remail/internal/governance/domain"
	"github.com/donnel666/remail/internal/mailbox"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

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

type iCloudDomainMailFileStore struct {
	files map[string]governancedomain.PrivateFile
	reads []string
}

func (s *iCloudDomainMailFileStore) SavePrivate(_ context.Context, file governancedomain.PrivateFile) (*governancedomain.StoredPrivateFile, error) {
	if s.files == nil {
		s.files = make(map[string]governancedomain.PrivateFile)
	}
	s.files[file.ObjectKey] = file
	return &governancedomain.StoredPrivateFile{ObjectKey: file.ObjectKey, FileName: file.FileName, ContentType: file.ContentType, Size: int64(len(file.ContentBytes))}, nil
}

func (s *iCloudDomainMailFileStore) SavePrivateStream(ctx context.Context, file governancedomain.PrivateFileStream) (*governancedomain.StoredPrivateFile, error) {
	content, err := io.ReadAll(file.Content)
	if err != nil {
		return nil, err
	}
	return s.SavePrivate(ctx, governancedomain.PrivateFile{ObjectKey: file.ObjectKey, FileName: file.FileName, ContentType: file.ContentType, ContentBytes: content})
}

func (s *iCloudDomainMailFileStore) ReadPrivate(_ context.Context, objectKey string) (*governancedomain.PrivateFile, error) {
	s.reads = append(s.reads, objectKey)
	file, ok := s.files[objectKey]
	if !ok {
		return nil, fmt.Errorf("missing object %s", objectKey)
	}
	clone := file
	clone.ContentBytes = bytes.Clone(file.ContentBytes)
	return &clone, nil
}

func (s *iCloudDomainMailFileStore) DeletePrivate(_ context.Context, objectKey string) error {
	delete(s.files, objectKey)
	return nil
}

func (*iCloudDomainMailFileStore) ListPrivate(context.Context, string, string, int) ([]governancedomain.PrivateObject, error) {
	return nil, nil
}

func TestDecodeICloudRelaySenderRequiresPersistedAliasID(t *testing.T) {
	envelope := "18005575_at_qq_com_zvzv72255gjx92_552k9812@icloud.com"
	anonymousID := "zvzv5gjx2k9812"
	recipientMailID := "zvzv72255gjx92_552k9812"

	if sender, ok := decodeICloudRelaySender(envelope, anonymousID); !ok || sender != "18005575@qq.com" {
		t.Fatalf("anonymous ID decode = %q, %v", sender, ok)
	}
	if sender, ok := decodeICloudRelaySenderForRoute(envelope, anonymousID, recipientMailID); !ok || sender != "18005575@qq.com" {
		t.Fatalf("route ID decode = %q, %v", sender, ok)
	}
	if sender, ok := decodeICloudRelaySenderForRoute(envelope, "unrelated-alias-id", recipientMailID); !ok || sender != "18005575@qq.com" {
		t.Fatalf("exact route ID must not depend on anonymous ID: %q, %v", sender, ok)
	}
	if _, ok := decodeICloudRelaySenderForRoute(envelope, anonymousID, "other_552k9812"); ok {
		t.Fatal("a different Apple recipient ID must not select the message")
	}
	if _, ok := decodeICloudRelaySender(envelope, "other552k9812"); ok {
		t.Fatal("a different anonymous ID must not select the message")
	}
}

func TestFetchMailSkipsKnownNewestRowAndReadsOlderMail(t *testing.T) {
	db := newICloudDomainMailTestDB(t, "icloud-domain-known-boundary")
	base := time.Date(2026, 8, 14, 8, 30, 0, 0, time.UTC)
	if err := db.Create(&iCloudAliasModel{
		ID: 5, ResourceID: 41, AnonymousID: "unrelated", Email: "first@icloud.com",
		ForwardToEmail: "relay@example.com", RecipientMailID: "recipient-route", Status: iCloudResourceNormal,
	}).Error; err != nil {
		t.Fatalf("create alias: %v", err)
	}
	files := &iCloudDomainMailFileStore{files: make(map[string]governancedomain.PrivateFile)}
	storeICloudInboundMail(t, db, files, iCloudInboundMailTestModel{
		ID: 1, EnvelopeFrom: "old_at_example_com_recipient-route@icloud.com", Recipient: "relay@example.com",
		ResourceType: "domain", SourceObjectKey: "mail/old.eml", Status: "stored", CreatedAt: base,
	}, true)
	storeICloudInboundMail(t, db, files, iCloudInboundMailTestModel{
		ID: 2, EnvelopeFrom: "new_at_example_com_recipient-route@icloud.com", Recipient: "relay@example.com",
		ResourceType: "domain", SourceObjectKey: "mail/new.eml", Status: "stored", CreatedAt: base.Add(time.Minute),
	}, false)

	result, err := NewService(db, nil, files).FetchMail(context.Background(), MailFetchRequest{
		ResourceID: 41, Recipient: "first@icloud.com", MaxMessages: 1,
		KnownMessageIDs: []string{"provider:smtp:inbound:inbound:2"},
	})
	if err != nil {
		t.Fatalf("fetch mail: %v", err)
	}
	if len(result.Messages) != 1 || result.Messages[0].ProviderMessageID != "inbound:1" || result.Messages[0].Sender != "old@example.com" {
		t.Fatalf("messages = %#v", result.Messages)
	}
	if len(files.reads) != 1 || files.reads[0] != "mail/old.eml" {
		t.Fatalf("private reads = %#v", files.reads)
	}
}

func TestFetchMailRejectsCrossResourceFallbackAmbiguity(t *testing.T) {
	db := newICloudDomainMailTestDB(t, "icloud-domain-global-ambiguity")
	base := time.Date(2026, 8, 14, 8, 45, 0, 0, time.UTC)
	aliases := []iCloudAliasModel{
		{ID: 5, ResourceID: 41, AnonymousID: "abc", Email: "first@icloud.com", ForwardToEmail: "relay@example.com", Status: iCloudResourceNormal},
		{ID: 6, ResourceID: 42, AnonymousID: "axbyc", Email: "other@icloud.com", ForwardToEmail: "relay@example.com", Status: iCloudResourceNormal},
	}
	if err := db.Create(&aliases).Error; err != nil {
		t.Fatalf("create aliases: %v", err)
	}
	files := &iCloudDomainMailFileStore{files: make(map[string]governancedomain.PrivateFile)}
	storeICloudInboundMail(t, db, files, iCloudInboundMailTestModel{
		ID: 1, EnvelopeFrom: "sender_at_example_com_axbyc@icloud.com", Recipient: "relay@example.com",
		ResourceType: "domain", SourceObjectKey: "mail/ambiguous.eml", Status: "stored", CreatedAt: base,
	}, false)

	result, err := NewService(db, nil, files).FetchMail(context.Background(), MailFetchRequest{
		ResourceID: 41, Recipient: "first@icloud.com", MaxMessages: 10,
	})
	if err != nil {
		t.Fatalf("fetch mail: %v", err)
	}
	if len(result.Messages) != 0 {
		t.Fatalf("ambiguous fallback messages = %#v", result.Messages)
	}
	if len(files.reads) != 0 {
		t.Fatalf("ambiguous mail must not be read: %#v", files.reads)
	}
}

func TestFetchMailReadsOnlyTheRequestedAliasAfterRouteMatch(t *testing.T) {
	db := newICloudDomainMailTestDB(t, "icloud-domain-order")
	base := time.Date(2026, 8, 14, 8, 0, 0, 0, time.UTC)
	aliases := []iCloudAliasModel{
		{ID: 5, ResourceID: 41, AnonymousID: "zvzv5gjx2k9812", Email: "first@icloud.com", ForwardToEmail: "relay@example.com", RecipientMailID: "zvzv72255gjx92_552k9812", Status: iCloudResourceNormal},
		{ID: 6, ResourceID: 41, AnonymousID: "secondalias", Email: "second@icloud.com", ForwardToEmail: "relay@example.com", RecipientMailID: "second_alias", Status: iCloudResourceNormal},
	}
	if err := db.Create(&aliases).Error; err != nil {
		t.Fatalf("create aliases: %v", err)
	}
	files := &iCloudDomainMailFileStore{files: make(map[string]governancedomain.PrivateFile)}
	storeICloudInboundMail(t, db, files, iCloudInboundMailTestModel{
		ID: 1, EnvelopeFrom: "first_at_example_com_zvzv72255gjx92_552k9812@icloud.com", Recipient: "relay@example.com",
		ResourceType: "domain", SourceObjectKey: "mail/first.eml", Status: "stored", CreatedAt: base,
	}, true)
	storeICloudInboundMail(t, db, files, iCloudInboundMailTestModel{
		ID: 2, EnvelopeFrom: "second_at_example_com_second_alias@icloud.com", Recipient: "relay@example.com",
		ResourceType: "domain", SourceObjectKey: "mail/second.eml", Status: "stored", CreatedAt: base.Add(time.Minute),
	}, false)

	result, err := NewService(db, nil, files).FetchMail(context.Background(), MailFetchRequest{
		ResourceID: 41, Recipient: "FIRST@ICLOUD.COM", SinceAt: base.Add(-time.Minute), UntilAt: base.Add(2 * time.Minute), MaxMessages: 10,
	})
	if err != nil {
		t.Fatalf("fetch mail: %v", err)
	}
	if len(result.Messages) != 1 || result.Messages[0].Recipient != "first@icloud.com" || result.Messages[0].Sender != "first@example.com" {
		t.Fatalf("messages = %#v", result.Messages)
	}
	if len(files.reads) != 1 || files.reads[0] != "mail/first.eml" {
		t.Fatalf("private reads = %#v", files.reads)
	}
}

func TestFetchMailReadsHistoricalRoutesAndRejectsAmbiguousAliasID(t *testing.T) {
	db := newICloudDomainMailTestDB(t, "icloud-domain-history")
	base := time.Date(2026, 8, 14, 9, 0, 0, 0, time.UTC)
	aliases := []iCloudAliasModel{
		{ID: 5, ResourceID: 41, AnonymousID: "recipient", Email: "history@icloud.com", ForwardToEmail: "new@example.com", Status: iCloudResourceNormal},
		{ID: 6, ResourceID: 41, AnonymousID: "longrecipient", Email: "ambiguous@icloud.com", ForwardToEmail: "new@example.com", Status: iCloudResourceNormal},
	}
	if err := db.Create(&aliases).Error; err != nil {
		t.Fatalf("create aliases: %v", err)
	}
	if err := db.Create(&iCloudAliasRouteModel{
		ResourceID: 41, AliasID: 5, ForwardToEmail: "old@example.com", RecipientMailID: "oldrecipient",
		FirstSeenAt: base.Add(-time.Hour), LastSeenAt: base,
	}).Error; err != nil {
		t.Fatalf("create route: %v", err)
	}
	files := &iCloudDomainMailFileStore{files: make(map[string]governancedomain.PrivateFile)}
	storeICloudInboundMail(t, db, files, iCloudInboundMailTestModel{
		ID: 1, EnvelopeFrom: "old_sender_at_example_com_oldrecipient@icloud.com", Recipient: "old@example.com",
		ResourceType: "domain", SourceObjectKey: "mail/old.eml", Status: "stored", CreatedAt: base,
	}, true)
	storeICloudInboundMail(t, db, files, iCloudInboundMailTestModel{
		ID: 2, EnvelopeFrom: "new_sender_at_example_com_newrecipient@icloud.com", Recipient: "new@example.com",
		ResourceType: "domain", SourceObjectKey: "mail/new.eml", Status: "stored", CreatedAt: base.Add(time.Minute),
	}, true)
	storeICloudInboundMail(t, db, files, iCloudInboundMailTestModel{
		ID: 3, EnvelopeFrom: "ambiguous_at_example_com_long_recipient@icloud.com", Recipient: "new@example.com",
		ResourceType: "domain", SourceObjectKey: "mail/ambiguous.eml", Status: "stored", CreatedAt: base.Add(2 * time.Minute),
	}, false)

	result, err := NewService(db, nil, files).FetchMail(context.Background(), MailFetchRequest{ResourceID: 41, FullHistory: true, MaxMessages: 10})
	if err != nil {
		t.Fatalf("fetch history: %v", err)
	}
	if len(result.Messages) != 2 || result.Messages[0].Sender != "old.sender@example.com" || result.Messages[1].Sender != "new.sender@example.com" {
		t.Fatalf("messages = %#v", result.Messages)
	}
	if len(files.reads) != 2 {
		t.Fatalf("ambiguous mail must not be read: %#v", files.reads)
	}
}

func newICloudDomainMailTestDB(t *testing.T, name string) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+name+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	if err := db.AutoMigrate(&iCloudAliasModel{}, &iCloudAliasRouteModel{}, &iCloudInboundMailTestModel{}); err != nil {
		t.Fatalf("migrate database: %v", err)
	}
	return db
}

func storeICloudInboundMail(t *testing.T, db *gorm.DB, files *iCloudDomainMailFileStore, row iCloudInboundMailTestModel, storeRaw bool) {
	t.Helper()
	row.MailboxKey = mailbox.Normalize(row.Recipient)
	if err := db.Create(&row).Error; err != nil {
		t.Fatalf("create inbound mail: %v", err)
	}
	if !storeRaw {
		return
	}
	_, err := files.SavePrivate(context.Background(), governancedomain.PrivateFile{
		ObjectKey:    row.SourceObjectKey,
		ContentBytes: []byte("From: original@example.net\r\nTo: " + row.Recipient + "\r\nSubject: test\r\n\r\nbody"),
	})
	if err != nil {
		t.Fatalf("store inbound object: %v", err)
	}
}
