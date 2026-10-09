package icloud

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"reflect"
	"slices"
	"strings"
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
	HeaderFrom      string    `gorm:"column:header_from"`
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

func TestFetchMailUsesHeaderSenderAndRestoresPlusRecipient(t *testing.T) {
	db := newICloudDomainMailTestDB(t, "icloud-domain-openai-plus")
	base := time.Date(2026, 8, 15, 17, 11, 43, 0, time.UTC)
	if err := db.Create(&iCloudAliasModel{
		ID: 238, ResourceID: 41, AnonymousID: "8m62jf69v89850", Email: "tile.mosses-7s@icloud.com",
		ForwardToEmail: "relay@example.com", Status: iCloudResourceNormal,
	}).Error; err != nil {
		t.Fatalf("create alias: %v", err)
	}
	files := &iCloudDomainMailFileStore{files: make(map[string]governancedomain.PrivateFile)}
	storeICloudInboundMail(t, db, files, iCloudInboundMailTestModel{
		ID:           1,
		EnvelopeFrom: "bounces+20216706-a5d4-tile.mosses-7s+t3tgpd=icloud.com_at_em7877_tm_openai_com_8m62jf69v89850_57152536@icloud.com",
		HeaderFrom:   "noreply_at_tm_openai_com_8m62jf69v89850_cd655749@icloud.com",
		Recipient:    "relay@example.com", ResourceType: "domain", SourceObjectKey: "mail/openai.eml",
		Status: "stored", CreatedAt: base,
	}, false)
	files.files["mail/openai.eml"] = governancedomain.PrivateFile{
		ObjectKey: "mail/openai.eml",
		ContentBytes: []byte("From: OpenAI <noreply_at_tm_openai_com_8m62jf69v89850_cd655749@icloud.com>\r\n" +
			"To: Hide My Email <tile.mosses-7s@icloud.com>\r\n" +
			"X-ICLOUD-HME: p=tile.mosses-7s@icloud.com; d=; f=relay@example.com; r=to; s=noreply@tm.openai.com\r\n" +
			"Subject: Your temporary OpenAI verification code\r\n\r\nCode 126515"),
	}

	result, err := NewService(db, nil, files).FetchMail(context.Background(), MailFetchRequest{
		ResourceID: 41, Recipient: "tile.mosses-7s@icloud.com", MaxMessages: 10,
	})
	if err != nil {
		t.Fatalf("fetch mail: %v", err)
	}
	if len(result.Messages) != 1 || result.Messages[0].Sender != "noreply@tm.openai.com" ||
		result.Messages[0].Recipient != "tile.mosses-7s+t3tgpd@icloud.com" {
		t.Fatalf("messages = %#v", result.Messages)
	}
	var plusAlias iCloudPlusAliasModel
	if err := db.Where("resource_id = ? AND email = ?", 41, "tile.mosses-7s+t3tgpd@icloud.com").Take(&plusAlias).Error; err != nil {
		t.Fatalf("load persisted plus alias: %v", err)
	}
	if plusAlias.AliasID != 238 || plusAlias.Status != iCloudResourceNormal {
		t.Fatalf("plus alias = %#v", plusAlias)
	}
	if _, err := NewService(db, nil, files).FetchMail(context.Background(), MailFetchRequest{
		ResourceID: 41, Recipient: "tile.mosses-7s@icloud.com", MaxMessages: 10,
	}); err != nil {
		t.Fatalf("fetch mail again: %v", err)
	}
	var plusCount int64
	if err := db.Model(&iCloudPlusAliasModel{}).Count(&plusCount).Error; err != nil || plusCount != 1 {
		t.Fatalf("plus aliases = %d, %v", plusCount, err)
	}
	var dotCount int64
	if err := db.Model(&iCloudDotAliasModel{}).Count(&dotCount).Error; err != nil || dotCount != 0 {
		t.Fatalf("dot aliases = %d, %v", dotCount, err)
	}
}

func TestPersistICloudAliasVariantsSplitsCombinedRecipient(t *testing.T) {
	db := newICloudDomainMailTestDB(t, "icloud-domain-dot-plus")
	if err := db.Create(&iCloudAliasModel{
		ID: 238, ResourceID: 41, AnonymousID: "8m62jf69v89850", Email: "tile.mosses-7s@icloud.com",
		ForwardToEmail: "relay@example.com", Status: iCloudResourceNormal,
	}).Error; err != nil {
		t.Fatalf("create alias: %v", err)
	}
	if err := NewService(db, nil, nil).persistICloudAliasVariants(context.Background(), []iCloudMailCandidate{{
		scope: iCloudMailAliasScope{
			ResourceID: 41, AliasID: 238, AliasEmail: "tile.mosses-7s@icloud.com",
		},
		recipient: "tilemosses-7s+t3tgpd@icloud.com",
	}}); err != nil {
		t.Fatalf("persist aliases: %v", err)
	}
	for table, email := range map[string]string{
		"icloud_dot_aliases":  "tilemosses-7s@icloud.com",
		"icloud_plus_aliases": "tilemosses-7s+t3tgpd@icloud.com",
	} {
		var row struct{ AliasID uint }
		if err := db.Table(table).Select("alias_id").Where("resource_id = ? AND email = ?", 41, email).Take(&row).Error; err != nil || row.AliasID != 238 {
			t.Fatalf("%s alias = %#v, %v", table, row, err)
		}
	}
}

func TestRestoreICloudRelayRecipientKeepsFullPlusAlias(t *testing.T) {
	envelope := "bounces+1-a5d4-tile.mosses-7s+tilemosses-7s=icloud.com_at_sender_example_com_8m62jf69v89850_57152536@icloud.com"
	if recipient := restoreICloudRelayRecipient(envelope, "tile.mosses-7s@icloud.com", "8m62jf69v89850", ""); recipient != "tile.mosses-7s+tilemosses-7s@icloud.com" {
		t.Fatalf("recipient = %q", recipient)
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

func TestFetchMailPreservesMailboxOwnershipAfterTargetMatch(t *testing.T) {
	for _, test := range []struct {
		name            string
		targetRecipient string
		otherRecipient  string
		relayTail       string
	}{
		{name: "exact route beats target fallback", otherRecipient: "axbyc", relayTail: "axbyc"},
		{name: "equal exact routes remain ambiguous", targetRecipient: "route", otherRecipient: "route", relayTail: "route"},
		{name: "longer exact route beats target route", targetRecipient: "route", otherRecipient: "long_route", relayTail: "long_route"},
	} {
		t.Run(test.name, func(t *testing.T) {
			db := newICloudDomainMailTestDB(t, "icloud-domain-target-"+strings.ReplaceAll(test.name, " ", "-"))
			aliases := []iCloudAliasModel{
				{ID: 5, ResourceID: 41, AnonymousID: "abc", Email: "first@icloud.com", ForwardToEmail: "relay@example.com", RecipientMailID: test.targetRecipient, Status: iCloudResourceNormal},
				{ID: 6, ResourceID: 42, AnonymousID: "axbyc", Email: "other@icloud.com", ForwardToEmail: "relay@example.com", RecipientMailID: test.otherRecipient, Status: iCloudResourceNormal},
			}
			if err := db.Create(&aliases).Error; err != nil {
				t.Fatalf("create aliases: %v", err)
			}
			files := &iCloudDomainMailFileStore{}
			storeICloudInboundMail(t, db, files, iCloudInboundMailTestModel{
				ID: 1, EnvelopeFrom: "sender_at_example_com_" + test.relayTail + "@icloud.com", Recipient: "relay@example.com",
				ResourceType: "domain", SourceObjectKey: "mail/other.eml", Status: "stored", CreatedAt: time.Now().UTC(),
			}, false)
			result, err := NewService(db, nil, files).FetchMail(context.Background(), MailFetchRequest{
				ResourceID: 41, Recipient: "first@icloud.com", MaxMessages: 1,
			})
			if err != nil {
				t.Fatalf("fetch mail: %v", err)
			}
			if len(result.Messages) != 0 || len(files.reads) != 0 {
				t.Fatalf("must not read another alias's mail: messages=%#v reads=%#v", result.Messages, files.reads)
			}
		})
	}
}

func TestFetchMailLimitsAfterComparingAllHistoricalMailboxes(t *testing.T) {
	db := newICloudDomainMailTestDB(t, "icloud-domain-multi-mailbox-limit")
	base := time.Date(2026, 8, 14, 10, 0, 0, 0, time.UTC)
	if err := db.Create(&iCloudAliasModel{
		ID: 5, ResourceID: 41, AnonymousID: "alias", Email: "first@icloud.com",
		ForwardToEmail: "new@example.com", RecipientMailID: "newroute", Status: iCloudResourceNormal,
	}).Error; err != nil {
		t.Fatalf("create alias: %v", err)
	}
	if err := db.Create(&iCloudAliasRouteModel{
		ResourceID: 41, AliasID: 5, ForwardToEmail: "old@example.com", RecipientMailID: "oldroute",
		FirstSeenAt: base.Add(-time.Hour), LastSeenAt: base,
	}).Error; err != nil {
		t.Fatalf("create route: %v", err)
	}
	files := &iCloudDomainMailFileStore{}
	for _, row := range []iCloudInboundMailTestModel{
		{ID: 1, EnvelopeFrom: "old_at_example_com_oldroute@icloud.com", Recipient: "old@example.com", CreatedAt: base},
		{ID: 2, EnvelopeFrom: "new_at_example_com_newroute@icloud.com", Recipient: "new@example.com", CreatedAt: base},
		{ID: 3, EnvelopeFrom: "older_at_example_com_oldroute@icloud.com", Recipient: "old@example.com", CreatedAt: base.Add(-time.Minute)},
	} {
		row.ResourceType, row.Status, row.SourceObjectKey = "domain", "stored", fmt.Sprintf("mail/%d.eml", row.ID)
		storeICloudInboundMail(t, db, files, row, row.ID == 2)
	}
	result, err := NewService(db, nil, files).FetchMail(context.Background(), MailFetchRequest{
		ResourceID: 41, Recipient: "first@icloud.com", MaxMessages: 1,
	})
	if err != nil {
		t.Fatalf("fetch mail: %v", err)
	}
	if len(result.Messages) != 1 || result.Messages[0].ProviderMessageID != "inbound:2" || len(files.reads) != 1 {
		t.Fatalf("newest message across mailboxes = %#v, reads=%#v", result.Messages, files.reads)
	}
}

func TestFetchMailPrefilterPreservesAliasRouteActivation(t *testing.T) {
	db := newICloudDomainMailTestDB(t, "icloud-domain-route-activation")
	base := time.Date(2026, 8, 14, 10, 0, 0, 0, time.UTC)
	if err := db.Create(&iCloudAliasModel{
		ID: 5, ResourceID: 41, AnonymousID: "alias", Email: "first@icloud.com",
		ForwardToEmail: "relay@example.com", RecipientMailID: "currentroute", Status: iCloudResourceNormal,
	}).Error; err != nil {
		t.Fatalf("create alias: %v", err)
	}
	if err := db.Create(&iCloudAliasRouteModel{
		ResourceID: 41, AliasID: 5, ForwardToEmail: "relay@example.com", RecipientMailID: "oldroute",
		FirstSeenAt: base.Add(time.Hour), LastSeenAt: base.Add(time.Hour),
	}).Error; err != nil {
		t.Fatalf("create route: %v", err)
	}
	files := &iCloudDomainMailFileStore{}
	storeICloudInboundMail(t, db, files, iCloudInboundMailTestModel{
		ID: 1, EnvelopeFrom: "sender_at_example_com_oldroute@icloud.com", Recipient: "relay@example.com",
		ResourceType: "domain", SourceObjectKey: "mail/old.eml", Status: "stored", CreatedAt: base,
	}, true)
	result, err := NewService(db, nil, files).FetchMail(context.Background(), MailFetchRequest{
		ResourceID: 41, Recipient: "first@icloud.com", SinceAt: base.Add(-time.Minute), MaxMessages: 1,
	})
	if err != nil {
		t.Fatalf("fetch mail: %v", err)
	}
	if len(result.Messages) != 1 || result.Messages[0].ProviderMessageID != "inbound:1" {
		t.Fatalf("alias with an active current route must retain historical recipient matching: %#v", result.Messages)
	}
}

func TestFetchMailPrefilterUsesLatestMailboxResolverRoute(t *testing.T) {
	db := newICloudDomainMailTestDB(t, "icloud-domain-concurrent-route-refresh")
	aliases := []iCloudAliasModel{
		{ID: 5, ResourceID: 41, AnonymousID: "alias", Email: "first@icloud.com", ForwardToEmail: "relay@example.com", RecipientMailID: "oldroute", Status: iCloudResourceNormal},
		{ID: 6, ResourceID: 42, AnonymousID: "other", Email: "other@icloud.com", ForwardToEmail: "relay@example.com", RecipientMailID: "otherroute", Status: iCloudResourceNormal},
	}
	if err := db.Create(&aliases).Error; err != nil {
		t.Fatalf("create alias: %v", err)
	}
	files := &iCloudDomainMailFileStore{}
	storeICloudInboundMail(t, db, files, iCloudInboundMailTestModel{
		ID: 1, EnvelopeFrom: "sender_at_example_com_newroute@icloud.com", Recipient: "relay@example.com",
		ResourceType: "domain", SourceObjectKey: "mail/new.eml", Status: "stored", CreatedAt: time.Now().UTC(),
	}, true)
	updated := false
	if err := db.Callback().Query().After("gorm:query").Register("icloud:test-refresh-route", func(tx *gorm.DB) {
		if tx.Statement.Table == "icloud_alias_routes" && !updated {
			updated = true
			tx.AddError(db.Model(&iCloudAliasModel{}).Where("id = ?", 5).Update("recipient_mail_id", "newroute").Error)
		}
	}); err != nil {
		t.Fatalf("register route refresh: %v", err)
	}
	result, err := NewService(db, nil, files).FetchMail(context.Background(), MailFetchRequest{
		ResourceID: 41, Recipient: "first@icloud.com", MaxMessages: 1,
	})
	if err != nil {
		t.Fatalf("fetch mail: %v", err)
	}
	if !updated || len(result.Messages) != 1 || result.Messages[0].ProviderMessageID != "inbound:1" {
		t.Fatalf("concurrent route update must not hide matching mail: updated=%v messages=%#v", updated, result.Messages)
	}
}

func TestLoadICloudMailboxResolverRoutesUsesForwardingIndexes(t *testing.T) {
	db := newICloudDomainMailTestDB(t, "icloud-domain-resolver-indexes")
	for _, sql := range []string{
		"CREATE INDEX idx_icloud_aliases_forward ON icloud_aliases (forward_to_email, status, id)",
		"CREATE UNIQUE INDEX uk_icloud_alias_routes_pair ON icloud_alias_routes (forward_to_email, recipient_mail_id)",
	} {
		if err := db.Exec(sql).Error; err != nil {
			t.Fatalf("create forwarding index: %v", err)
		}
	}
	type query struct {
		sql  string
		args []any
	}
	var queries []query
	if err := db.Callback().Query().After("gorm:query").Register("icloud:test-capture-resolver", func(tx *gorm.DB) {
		queries = append(queries, query{sql: tx.Statement.SQL.String(), args: slices.Clone(tx.Statement.Vars)})
	}); err != nil {
		t.Fatalf("capture resolver queries: %v", err)
	}
	if _, err := NewService(db, nil, nil).loadICloudMailboxResolverRoutes(context.Background(), "relay@example.com"); err != nil {
		t.Fatalf("load resolver routes: %v", err)
	}
	if len(queries) != 2 {
		t.Fatalf("resolver queries = %d, want current and historical routes", len(queries))
	}
	for index, query := range queries {
		var plan []struct{ Detail string }
		if err := db.Raw("EXPLAIN QUERY PLAN "+query.sql, query.args...).Scan(&plan).Error; err != nil {
			t.Fatalf("explain resolver query: %v", err)
		}
		want := []string{"idx_icloud_aliases_forward", "uk_icloud_alias_routes_pair"}[index]
		if !slices.ContainsFunc(plan, func(step struct{ Detail string }) bool { return strings.Contains(step.Detail, want) }) {
			t.Fatalf("resolver must use %s: plan=%#v sql=%s", want, plan, query.sql)
		}
	}
}

func TestFetchMailStopsScanningWhenMailboxLimitIsMet(t *testing.T) {
	db := newICloudDomainMailTestDB(t, "icloud-domain-scan-limit")
	aliases := []iCloudAliasModel{
		{ID: 5, ResourceID: 41, AnonymousID: "target", Email: "first@icloud.com", ForwardToEmail: "relay@example.com", Status: iCloudResourceNormal},
		{ID: 6, ResourceID: 42, AnonymousID: "other", Email: "other@icloud.com", ForwardToEmail: "relay@example.com", Status: iCloudResourceNormal},
	}
	if err := db.Create(&aliases).Error; err != nil {
		t.Fatalf("create aliases: %v", err)
	}
	files := &iCloudDomainMailFileStore{}
	for id := uint(1); id <= 5; id++ {
		storeICloudInboundMail(t, db, files, iCloudInboundMailTestModel{
			ID: id, EnvelopeFrom: "sender_at_example_com_target@icloud.com", Recipient: "relay@example.com",
			ResourceType: "domain", SourceObjectKey: fmt.Sprintf("mail/%d.eml", id), Status: "stored", CreatedAt: time.Now().UTC(),
		}, true)
	}
	statement := &gorm.Statement{DB: db}
	if err := statement.Parse(&iCloudForwardedMailRow{}); err != nil {
		t.Fatalf("parse scanned row schema: %v", err)
	}
	field := statement.Schema.LookUpField("ID")
	set := field.Set
	scanned := 0
	// Count decoded rows in this database's schema, independently of the returned result limit.
	field.Set = func(ctx context.Context, value reflect.Value, input any) error {
		scanned++
		return set(ctx, value, input)
	}
	for _, limit := range []int{1, 2} {
		scanned = 0
		result, err := NewService(db, nil, files).FetchMail(context.Background(), MailFetchRequest{
			ResourceID: 41, Recipient: "first@icloud.com", MaxMessages: limit,
		})
		if err != nil {
			t.Fatalf("fetch mail: %v", err)
		}
		if len(result.Messages) != limit || scanned != limit || result.Messages[limit-1].ProviderMessageID != "inbound:5" {
			t.Fatalf("limit=%d messages=%#v scanned=%d", limit, result.Messages, scanned)
		}
	}
}

func BenchmarkICloudFetchMail(b *testing.B) {
	for _, test := range []struct {
		name            string
		mailboxAliases  int
		targetAliases   int
		recipientRoutes int
		rows            int
		limit           int
		fullHistory     bool
		wantMessages    int
	}{
		{name: "single/no-hit", mailboxAliases: 750, targetAliases: 1, rows: 1000, limit: 1},
		{name: "single/latest-hit", mailboxAliases: 750, targetAliases: 1, rows: 1000, limit: 1, wantMessages: 1},
		{name: "single/whole-mailbox", mailboxAliases: 1, targetAliases: 1, rows: 1000, limit: 1000, wantMessages: 1000},
		{name: "history/all-aliases", mailboxAliases: 750, targetAliases: 750, rows: 5000, limit: 5000, fullHistory: true, wantMessages: 5000},
		{name: "history/almost-all-aliases", mailboxAliases: 751, targetAliases: 750, rows: 5000, limit: 5000, fullHistory: true, wantMessages: 5000},
		{name: "history/one-alias-many-routes", mailboxAliases: 2, targetAliases: 1, recipientRoutes: 750, rows: 5000, limit: 5000, fullHistory: true, wantMessages: 5000},
	} {
		b.Run(test.name, func(b *testing.B) {
			db := newICloudDomainMailTestDB(b, "icloud-fetch-bench-"+strings.ReplaceAll(test.name, "/", "-"))
			aliases := make([]iCloudAliasModel, test.mailboxAliases)
			for index := range aliases {
				resourceID := uint(41)
				if index >= test.targetAliases {
					resourceID = 42
				}
				aliases[index] = iCloudAliasModel{
					ID: uint(index + 1), ResourceID: resourceID, AnonymousID: fmt.Sprintf("alias%04d", index),
					Email: fmt.Sprintf("alias%04d@icloud.com", index), ForwardToEmail: "relay@example.com", Status: iCloudResourceNormal,
				}
			}
			if test.recipientRoutes > 0 {
				aliases[0].RecipientMailID = "recipient0000"
			}
			if err := db.Create(&aliases).Error; err != nil {
				b.Fatalf("create aliases: %v", err)
			}
			files := &iCloudDomainMailFileStore{}
			base := time.Date(2026, 8, 14, 10, 0, 0, 0, time.UTC)
			if test.recipientRoutes > 0 {
				routes := make([]iCloudAliasRouteModel, test.recipientRoutes)
				for index := range routes {
					routes[index] = iCloudAliasRouteModel{
						ResourceID: 41, AliasID: aliases[0].ID, ForwardToEmail: "relay@example.com", RecipientMailID: fmt.Sprintf("recipient%04d", index),
						FirstSeenAt: base.Add(-time.Hour), LastSeenAt: base,
					}
				}
				if err := db.Create(&routes).Error; err != nil {
					b.Fatalf("create historical recipient routes: %v", err)
				}
			}
			for index := 0; index < test.rows; index++ {
				aliasIndex := index % test.targetAliases
				if test.wantMessages == 0 {
					aliasIndex = test.mailboxAliases - 1
				}
				relayTail := aliases[aliasIndex].AnonymousID
				if test.recipientRoutes > 0 {
					relayTail = fmt.Sprintf("recipient%04d", index%test.recipientRoutes)
				}
				storeICloudInboundMail(b, db, files, iCloudInboundMailTestModel{
					ID: uint(index + 1), EnvelopeFrom: "sender_at_example_com_" + relayTail + "@icloud.com",
					Recipient: "relay@example.com", ResourceType: "domain", SourceObjectKey: fmt.Sprintf("mail/%d.eml", index),
					Status: "stored", CreatedAt: base.Add(time.Duration(index) * time.Second),
				}, test.wantMessages > 0)
			}
			service := NewService(db, nil, files)
			request := MailFetchRequest{ResourceID: 41, Recipient: aliases[0].Email, MaxMessages: test.limit, FullHistory: test.fullHistory}
			latestID := fmt.Sprintf("inbound:%d", test.rows)
			b.ReportAllocs()
			b.ResetTimer()
			for iteration := 0; iteration < b.N; iteration++ {
				files.reads = files.reads[:0]
				result, err := service.FetchMail(context.Background(), request)
				if err != nil {
					b.Fatalf("fetch mail: %v", err)
				}
				if len(result.Messages) != test.wantMessages || len(files.reads) != test.wantMessages {
					b.Fatalf("messages=%d reads=%d want=%d", len(result.Messages), len(files.reads), test.wantMessages)
				}
				if test.wantMessages > 0 && result.Messages[len(result.Messages)-1].ProviderMessageID != latestID {
					b.Fatalf("latest message = %#v", result.Messages[len(result.Messages)-1])
				}
			}
		})
	}
}

func newICloudDomainMailTestDB(t testing.TB, name string) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+name+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("get database connection: %v", err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err := db.AutoMigrate(
		&iCloudAliasModel{}, &iCloudDotAliasModel{}, &iCloudPlusAliasModel{},
		&iCloudAliasRouteModel{}, &iCloudInboundMailTestModel{},
	); err != nil {
		t.Fatalf("migrate database: %v", err)
	}
	return db
}

func storeICloudInboundMail(t testing.TB, db *gorm.DB, files *iCloudDomainMailFileStore, row iCloudInboundMailTestModel, storeRaw bool) {
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
