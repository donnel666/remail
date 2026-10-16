package infra

import (
	"context"
	"fmt"
	"testing"
	"time"

	governanceapp "github.com/donnel666/remail/internal/governance/app"
	governancedomain "github.com/donnel666/remail/internal/governance/domain"
	"github.com/donnel666/remail/internal/icloud"
	"github.com/donnel666/remail/internal/mailmatch/app"
	"github.com/donnel666/remail/internal/mailmatch/domain"
	mailinfra "github.com/donnel666/remail/internal/mailtransport/infra"
	"github.com/stretchr/testify/require"
)

type receiptPrivateFile struct {
	governanceapp.FilePort
	raw []byte
}

func (f receiptPrivateFile) ReadPrivate(_ context.Context, key string) (*governancedomain.PrivateFile, error) {
	return &governancedomain.PrivateFile{ObjectKey: key, ContentBytes: f.raw}, nil
}

func TestLivenessReceiptAppearsInResourceInboxWithoutManualFetchMySQL(t *testing.T) {
	db := newMailmatchMySQLTestDB(t)
	seedMailmatchOrder(t, db, "OR_RECEIPT_TEST")
	now := time.Now().UTC().Truncate(time.Millisecond)
	started := now.Add(-time.Minute)
	require.NoError(t, db.Exec("INSERT INTO email_resources(id,type,owner_user_id) VALUES(101,'icloud',1)").Error)
	require.NoError(t, db.Exec("INSERT INTO email_resources(id,type,owner_user_id) VALUES(102,'domain',1)").Error)
	require.NoError(t, db.Exec(`INSERT INTO icloud_resources(id,primary_email,status,credential_revision,validation_generation,expire_at)
        VALUES(101,'owner@example.com','abnormal',1,1,?)`, now.Add(24*time.Hour)).Error)
	require.NoError(t, db.Exec(`INSERT INTO icloud_aliases(resource_id,anonymous_id,email,forward_to_email,status)
        VALUES(101,'abc','alias@icloud.com','relay@example.com','normal')`).Error)
	require.NoError(t, db.Exec(`INSERT INTO icloud_maintenance_runs(id,resource_id,kind,status,attempts,max_attempts,credential_revision,validation_generation,
        probe_sender,probe_recipient,queued_at,started_at,probe_sent_at,next_check_at)
        VALUES(901,101,'liveness','running',1,1,1,1,'probe@relay.example','alias@icloud.com',?,?,?,?)`, started, started, started, started).Error)
	subject := "ReMail iCloud liveness probe@relay.example"
	files := receiptPrivateFile{raw: []byte("From: probe_at_relay_example_abc@icloud.com\r\nTo: alias@icloud.com\r\nX-ICLOUD-HME: p=alias@icloud.com; s=probe@relay.example\r\nSubject: " + subject + "\r\n\r\nprobe")}
	inbound := mailinfra.InboundMailModel{
		ResourceID: 102, ResourceType: "domain", OwnerUserID: 1, Recipient: "relay@example.com", MailboxKey: "relay@example.com",
		EnvelopeFrom: "probe_at_relay_example_abc@icloud.com", HeaderFrom: "probe_at_relay_example_abc@icloud.com",
		Subject: subject, SourceObjectKey: "probe", Status: "stored", CreatedAt: now, UpdatedAt: now,
	}
	require.NoError(t, db.Create(&inbound).Error)
	service := icloud.NewService(db, nil, files)
	messages := app.NewUseCase(NewRepo(db, nil), nil, nil, nil)
	service.SetLivenessReceiptStore(messages)
	ctx := context.Background()
	require.NoError(t, service.ProcessICloudLivenessCheck(ctx, 901))
	var state struct{ Status string }
	require.NoError(t, db.Table("icloud_maintenance_runs").Select("status").Where("id=901").Take(&state).Error)
	require.Equal(t, "succeeded", state.Status)
	items, total, _, err := NewAdminMessageRepo(db).ListAdminMessageSummaries(ctx, app.AdminMessageListQuery{
		ResourceID: 101, ResourceType: domain.ResourceTypeICloud, Limit: 20,
	})
	require.NoError(t, err)
	require.Equal(t, int64(1), total)
	require.Len(t, items, 1)
	require.Equal(t, "alias@icloud.com", items[0].Recipient)
	require.Equal(t, "probe@relay.example", items[0].Sender)
	require.Equal(t, subject, items[0].Subject)
	require.Equal(t, "ignored", string(items[0].Status))

	// The same receipt, including a later explicit fetch, resolves to the same stored fact.
	require.NoError(t, service.ProcessICloudLivenessCheck(ctx, 901))
	_, _, err = messages.IngestInboundMailWithFence(ctx, app.InboundMailRequest{
		EmailResourceID: 101, ResourceType: domain.ResourceTypeICloud, Recipient: "alias@icloud.com", EnvelopeFrom: "probe@relay.example",
		Raw: files.raw, ReceivedAt: now, ProviderMessageID: fmt.Sprintf("inbound:%d", inbound.ID), Protocol: "smtp", Folder: "inbound",
	}, func(context.Context) error { return nil })
	require.NoError(t, err)
	var count int64
	require.NoError(t, db.Table("mailmatch_messages").Where("email_resource_id=101").Count(&count).Error)
	require.Equal(t, int64(1), count)
}
