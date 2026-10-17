package infra

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	governancedomain "github.com/donnel666/remail/internal/governance/domain"
	mailmatchapp "github.com/donnel666/remail/internal/mailmatch/app"
	mailmatchdomain "github.com/donnel666/remail/internal/mailmatch/domain"
	mailmatchinfra "github.com/donnel666/remail/internal/mailmatch/infra"
	mailapp "github.com/donnel666/remail/internal/mailtransport/app"
	"github.com/donnel666/remail/internal/mailtransport/domain"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// Only the provider-independent scope loader is replaced to avoid MySQL-specific
// pickup SQL; message selection and pickup body reads use the real MailMatch repository.
type daumSourceScopeRepo struct {
	*mailmatchinfra.Repo
	db        *gorm.DB
	afterPage func()
}

func (r *daumSourceScopeRepo) LoadPickupScope(ctx context.Context, token, email string) (*mailmatchapp.OrderScope, error) {
	var row struct {
		UserID                         uint
		ServiceMode, Status            string
		ReceiveStartedAt, ReceiveUntil *time.Time
	}
	if err := r.db.WithContext(ctx).Table("orders").Where("order_no='original-order'").Take(&row).Error; err != nil {
		return nil, err
	}
	if token != "pickup-secret" || email != "helper@outlook.com" {
		return nil, mailmatchdomain.ErrPickupCredentialInvalid
	}
	return &mailmatchapp.OrderScope{OrderID: 10, OrderNo: "original-order", UserID: row.UserID, EmailResourceID: 50, Recipient: email, AllocationType: mailmatchdomain.ResourceTypeMicrosoft, ServiceMode: row.ServiceMode, OrderStatus: row.Status, ReceiveStartedAt: row.ReceiveStartedAt, ReceiveUntil: row.ReceiveUntil}, nil
}

func (r *daumSourceScopeRepo) ListOrderMessagePage(ctx context.Context, scope mailmatchapp.OrderScope, query mailmatchapp.AdminMessageListQuery) ([]mailmatchapp.AdminMessageSummary, int64, bool, error) {
	rows, total, more, err := r.Repo.ListOrderMessagePage(ctx, scope, query)
	if r.afterPage != nil {
		r.afterPage()
	}
	return rows, total, more, err
}

type daumRevokingPickup struct {
	*mailmatchapp.UseCase
	revoke func()
}

func (r daumRevokingPickup) GetPickupMessage(ctx context.Context, token, email string, id uint) (*mailmatchdomain.MailContent, error) {
	message, err := r.UseCase.GetPickupMessage(ctx, token, email, id)
	r.revoke()
	return message, err
}

type daumSourceAudit struct{ count int }

func (a *daumSourceAudit) Create(context.Context, *governancedomain.OperationLog) error {
	a.count++
	return nil
}

func TestDaumAuxiliaryUsesSourceOrderProjectionAndFullPagination(t *testing.T) {
	db := newDaumAuxiliaryTestDB(t, true)
	for _, statement := range []string{
		`CREATE TABLE microsoft_resources(id INTEGER PRIMARY KEY,status TEXT)`,
		`CREATE TABLE microsoft_allocations(order_no TEXT PRIMARY KEY,resource_id INTEGER,email TEXT,status TEXT)`,
		`INSERT INTO microsoft_resources VALUES(50,'normal')`,
		`INSERT INTO microsoft_allocations VALUES('original-order',50,'helper@outlook.com','allocated')`,
		`UPDATE email_resources SET type='microsoft' WHERE id=50`,
		`UPDATE orders SET allocation_type='microsoft',delivery_email='helper@outlook.com'`,
		`UPDATE daum_binding_mailboxes SET domain_resource_id=NULL,mailbox_resource_id=50,binding_address='helper@outlook.com',pickup_url='https://remail.test/v1/pickup?email=helper%40outlook.com&token=pickup-secret'`,
		`UPDATE inbound_mails SET recipient='helper@outlook.com',header_from='security@unrelated-project.test',subject='Unrelated raw secret',verification_code='654321' WHERE id=1`,
	} {
		require.NoError(t, db.Exec(statement).Error)
	}
	require.NoError(t, db.AutoMigrate(&mailmatchinfra.MessageModel{}, &mailmatchinfra.MessageProjectionModel{}, &mailmatchinfra.OrderDeliveryHeadModel{}))
	now := time.Now().UTC().Truncate(time.Second)
	owner, other := uint(10), uint(20)
	for id := uint(1); id <= 44; id++ {
		assigned := owner
		if id == 42 || id == 44 {
			assigned = other
		}
		require.NoError(t, db.Create(&mailmatchinfra.MessageModel{ID: id, ResourceType: "microsoft", EmailResourceID: 50, Recipient: "helper@outlook.com", Sender: "kakao@example.test", Subject: "Source order message", RawBody: sql.NullString{String: fmt.Sprintf("source body %d", id), Valid: true}, BodyPreview: "source preview", VerificationCode: "123456", DedupeKey: fmt.Sprintf("%064d", id), MatchedOrderID: &assigned, Status: "matched", ReceivedAt: now}).Error)
	}
	require.NoError(t, db.Create(&mailmatchinfra.MessageProjectionModel{MessageID: 43, MatchedOrderID: &other, Status: "matched", MessageReceivedAt: now}).Error)
	require.NoError(t, db.Create(&mailmatchinfra.MessageProjectionModel{MessageID: 44, MatchedOrderID: &owner, Status: "matched", MessageReceivedAt: now}).Error)
	repo := NewAuxiliaryMailRepo(db)
	source := &daumSourceScopeRepo{Repo: mailmatchinfra.NewRepo(db, nil), db: db}
	audit := &daumSourceAudit{}
	service := mailapp.NewAuxiliaryMailQueryService(repo, NewMicrosoftBindingRepo(db), nil, audit, nil)
	service.SetDaumPickupSource(source, mailmatchapp.NewUseCase(source, nil, nil, nil))
	filter := mailapp.AuxiliaryMailFilter{ResourceType: domain.InboundResourceDaum, ResourceID: 42, Limit: 20}
	page, err := service.List(context.Background(), filter)
	require.NoError(t, err)
	require.EqualValues(t, 42, page.Total)
	require.Len(t, page.Items, 20)
	require.True(t, page.HasMore)
	require.EqualValues(t, 44, page.Items[0].ID)
	ids := []uint{}
	for _, row := range page.Items {
		ids = append(ids, row.ID)
		if row.VerificationCode != nil {
			require.NotEqual(t, "654321", *row.VerificationCode)
		}
	}
	filter.BeforeReceivedAt, filter.BeforeID, filter.SkipTotal = page.NextBeforeReceivedAt, page.NextBeforeID, true
	page, err = service.List(context.Background(), filter)
	require.NoError(t, err)
	require.Len(t, page.Items, 20)
	require.True(t, page.HasMore)
	for _, row := range page.Items {
		ids = append(ids, row.ID)
	}
	filter.BeforeReceivedAt, filter.BeforeID = page.NextBeforeReceivedAt, page.NextBeforeID
	page, err = service.List(context.Background(), filter)
	require.NoError(t, err)
	require.Len(t, page.Items, 2)
	require.False(t, page.HasMore)
	for _, row := range page.Items {
		ids = append(ids, row.ID)
	}
	require.Len(t, ids, 42)
	require.NotContains(t, ids, uint(42))
	require.NotContains(t, ids, uint(43))
	request := mailapp.AuxiliaryMailDetailRequest{ResourceType: domain.InboundResourceDaum, ResourceID: 42, MessageID: 44, OperatorUserID: 1}
	detail, err := service.Get(context.Background(), request)
	require.NoError(t, err)
	require.Equal(t, "source body 44", detail.Body)
	require.Equal(t, 1, audit.count)
	for _, id := range []uint{42, 43} {
		request.MessageID = id
		_, err = service.Get(context.Background(), request)
		require.ErrorIs(t, err, domain.ErrAuxiliaryMessageNotFound)
	}
	require.Equal(t, 1, audit.count)
	require.NoError(t, db.Exec(`UPDATE order_tokens SET enabled=FALSE`).Error)
	filter.BeforeReceivedAt, filter.BeforeID = nil, 0
	page, err = service.List(context.Background(), filter)
	require.NoError(t, err)
	require.Empty(t, page.Items)
	request.MessageID = 44
	_, err = service.Get(context.Background(), request)
	require.ErrorIs(t, err, domain.ErrAuxiliaryMessageNotFound)
	raw, _, _, err := repo.ListMessages(context.Background(), filter)
	require.NoError(t, err)
	require.Empty(t, raw, "URL links must not fall back to unscoped inbound messages")
	require.NoError(t, db.Exec(`UPDATE order_tokens SET enabled=TRUE`).Error)
	source.afterPage = func() { require.NoError(t, db.Exec(`UPDATE daum_binding_mailboxes SET status='conflict'`).Error) }
	page, err = service.List(context.Background(), filter)
	require.NoError(t, err)
	require.Empty(t, page.Items, "a revoked binding after page query must discard the result")
	source.afterPage = nil
	require.NoError(t, db.Exec(`UPDATE daum_binding_mailboxes SET status='linked'`).Error)
	service.SetDaumPickupSource(source, daumRevokingPickup{UseCase: mailmatchapp.NewUseCase(source, nil, nil, nil), revoke: func() { require.NoError(t, db.Exec(`UPDATE order_tokens SET enabled=FALSE`).Error) }})
	_, err = service.Get(context.Background(), request)
	require.ErrorIs(t, err, domain.ErrAuxiliaryMessageNotFound)
	require.Equal(t, 1, audit.count, "revoked body is not returned or audited as a successful read")

}
