package app

import (
	"context"
	"encoding/base64"
	"testing"
	"time"

	"github.com/donnel666/remail/internal/aftersale/domain"
)

// ---------------------------------------------------------------------------
// Fakes
// ---------------------------------------------------------------------------

type fakeRepo struct {
	ticket      *domain.Ticket
	createCalls []CreateTicketParams
	replyCalls  []ReplyParams
	closeCalls  []CloseParams
	readCalls   []bool
	attachment  *domain.TicketAttachment
}

func (f *fakeRepo) Create(_ context.Context, params CreateTicketParams) (*domain.Ticket, error) {
	f.createCalls = append(f.createCalls, params)
	f.ticket = &domain.Ticket{
		TicketNo:            params.TicketNo,
		TicketType:          params.TicketType,
		Title:               params.Title,
		Status:              domain.TicketStatusOpen,
		RequesterUserID:     params.RequesterUserID,
		Order:               params.Order,
		PlatformUnreadCount: 1,
	}
	return f.ticket, nil
}

func (f *fakeRepo) Get(_ context.Context, ticketNo string, _ bool) (*domain.Ticket, error) {
	if f.ticket == nil || f.ticket.TicketNo != ticketNo {
		return nil, domain.ErrTicketNotFound
	}
	return f.ticket, nil
}

func (f *fakeRepo) List(context.Context, ListFilter, int, uint, int) ([]domain.Ticket, *uint, error) {
	return nil, nil, nil
}

func (f *fakeRepo) Count(context.Context, ListFilter) (int64, error) { return 0, nil }

func (f *fakeRepo) Facets(context.Context, ListFilter) (*TicketFacets, error) {
	return &TicketFacets{}, nil
}

func (f *fakeRepo) Reply(_ context.Context, params ReplyParams) (*domain.Ticket, error) {
	f.replyCalls = append(f.replyCalls, params)
	if params.Message.SenderType == domain.SenderTypePlatform && f.ticket.Status == domain.TicketStatusOpen {
		f.ticket.Status = domain.TicketStatusProcessing
	}
	return f.ticket, nil
}

func (f *fakeRepo) MarkRead(_ context.Context, _ string, platformSide bool) (*domain.Ticket, error) {
	f.readCalls = append(f.readCalls, platformSide)
	return f.ticket, nil
}

func (f *fakeRepo) Close(_ context.Context, params CloseParams) (*domain.Ticket, error) {
	f.closeCalls = append(f.closeCalls, params)
	f.ticket.Status = domain.TicketStatusClosed
	resolution := params.Resolution
	f.ticket.Resolution = &resolution
	return f.ticket, nil
}

func (f *fakeRepo) FindAttachment(context.Context, string, string) (*domain.TicketAttachment, error) {
	if f.attachment == nil {
		return nil, domain.ErrAttachmentNotFound
	}
	return f.attachment, nil
}

type fakeOrderPort struct {
	info *OrderInfo
	err  error
}

func (f fakeOrderPort) GetOrderForTicket(context.Context, string, uint) (*OrderInfo, error) {
	return f.info, f.err
}

type fakeRefundPort struct {
	called *RefundCommand
	result *RefundResult
	err    error
}

func (f *fakeRefundPort) RefundOrder(_ context.Context, cmd RefundCommand) (*RefundResult, error) {
	f.called = &cmd
	if f.err != nil {
		return nil, f.err
	}
	return f.result, nil
}

type fakeFileStore struct{ saved []string }

func (f *fakeFileStore) Save(_ context.Context, objectKey, _, _ string, _ []byte) error {
	f.saved = append(f.saved, objectKey)
	return nil
}

func (f *fakeFileStore) Read(context.Context, string) (string, []byte, error) {
	return "image/png", []byte("x"), nil
}

type fakeOwners struct{}

func (fakeOwners) GetByIDs(_ context.Context, ids []uint) (map[uint]RequesterSummary, error) {
	out := make(map[uint]RequesterSummary, len(ids))
	for _, id := range ids {
		out[id] = RequesterSummary{ID: id, Nickname: "nick", Email: "u@example.com"}
	}
	return out, nil
}

func newTestUseCase() (*UseCase, *fakeRepo, *fakeRefundPort, *fakeFileStore) {
	repo := &fakeRepo{}
	refund := &fakeRefundPort{result: &RefundResult{RefundAmount: "6.8"}}
	files := &fakeFileStore{}
	uc := NewUseCase(repo, fakeOrderPort{}, refund, files)
	uc.SetOwnerLookupPort(fakeOwners{})
	uc.now = func() time.Time { return time.Date(2026, 7, 17, 0, 0, 0, 0, time.UTC) }
	return uc, repo, refund, files
}

func pngDataURL(payload string) string {
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString([]byte(payload))
}

// ---------------------------------------------------------------------------
// Pure helpers
// ---------------------------------------------------------------------------

func TestDecodeImageDataURL(t *testing.T) {
	mime, data, err := decodeImageDataURL(pngDataURL("hello"))
	if err != nil || mime != "image/png" || string(data) != "hello" {
		t.Fatalf("valid data url: mime=%q data=%q err=%v", mime, data, err)
	}
	for name, raw := range map[string]string{
		"not data url": "https://example.com/a.png",
		"not base64":   "data:image/png,hello",
		"not image":    "data:text/plain;base64,aGVsbG8=",
		"bad base64":   "data:image/png;base64,!!!!",
	} {
		if _, _, err := decodeImageDataURL(raw); err == nil {
			t.Errorf("%s: expected error, got nil", name)
		}
	}
}

func TestFormatMoney(t *testing.T) {
	cases := map[string]string{"6.800000": "¥6.8", "0": "¥0", "": "¥0", "10.000000": "¥10", "1.500000": "¥1.5"}
	for in, want := range cases {
		if got := formatMoney(in); got != want {
			t.Errorf("formatMoney(%q)=%q want %q", in, got, want)
		}
	}
}

func TestCheckOrderEligibility(t *testing.T) {
	uc, _, _, _ := newTestUseCase()
	future := uc.now().Add(24 * time.Hour)
	past := uc.now().Add(-24 * time.Hour)
	cases := []struct {
		name     string
		info     OrderInfo
		eligible bool
	}{
		{"refunded", OrderInfo{Status: "refunded"}, false},
		{"pending", OrderInfo{Status: "pending_payment"}, false},
		{"failed", OrderInfo{Status: "failed"}, false},
		{"active", OrderInfo{Status: "active"}, true},
		{"completed within window", OrderInfo{Status: "completed", AfterSaleUntil: &future}, true},
		{"completed expired", OrderInfo{Status: "completed", AfterSaleUntil: &past}, false},
		{"completed no window", OrderInfo{Status: "completed"}, false},
	}
	for _, c := range cases {
		info := c.info
		err := uc.checkOrderEligibility(&info)
		if c.eligible != (err == nil) {
			t.Errorf("%s: eligible=%v err=%v", c.name, c.eligible, err)
		}
	}
}

// ---------------------------------------------------------------------------
// Orchestration
// ---------------------------------------------------------------------------

func TestCreateTicketGeneral(t *testing.T) {
	uc, repo, _, files := newTestUseCase()
	view, err := uc.CreateTicket(context.Background(), CreateTicketRequest{
		RequesterUserID: 7,
		TicketType:      domain.TicketTypeGeneral,
		Title:           "help",
		FirstMessage:    "hi",
		Attachments:     []string{pngDataURL("a"), pngDataURL("b")},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if view.Ticket.Status != domain.TicketStatusOpen || view.Ticket.Order != nil {
		t.Fatalf("unexpected ticket: %+v", view.Ticket)
	}
	if len(files.saved) != 2 {
		t.Fatalf("expected 2 attachments uploaded, got %d", len(files.saved))
	}
	msg := repo.createCalls[0].FirstMessage
	if msg.SenderType != domain.SenderTypeUser || len(msg.Attachments) != 2 {
		t.Fatalf("first message wrong: %+v", msg)
	}
	if view.Requester == nil || view.Requester.Nickname != "nick" {
		t.Fatalf("requester not enriched: %+v", view.Requester)
	}
}

func TestCreateTicketOrderEligibility(t *testing.T) {
	uc, _, _, _ := newTestUseCase()
	future := uc.now().Add(24 * time.Hour)
	uc.orders = fakeOrderPort{info: &OrderInfo{
		OrderNo: "OR1", ProjectName: "Telegram", Status: "active",
		PayAmount: "6.8", DeliveryEmail: "a@b.com", AfterSaleUntil: &future,
	}}
	view, err := uc.CreateTicket(context.Background(), CreateTicketRequest{
		RequesterUserID: 7, TicketType: domain.TicketTypeOrder, Title: "t", FirstMessage: "m", OrderNo: "OR1",
	})
	if err != nil {
		t.Fatalf("create order ticket: %v", err)
	}
	if view.Ticket.Order == nil || view.Ticket.Order.OrderNo != "OR1" || view.Ticket.Order.ProjectName != "Telegram" {
		t.Fatalf("snapshot missing: %+v", view.Ticket.Order)
	}

	uc.orders = fakeOrderPort{info: &OrderInfo{OrderNo: "OR2", Status: "refunded"}}
	if _, err := uc.CreateTicket(context.Background(), CreateTicketRequest{
		RequesterUserID: 7, TicketType: domain.TicketTypeOrder, Title: "t", FirstMessage: "m", OrderNo: "OR2",
	}); err != domain.ErrOrderNotEligible {
		t.Fatalf("expected ErrOrderNotEligible, got %v", err)
	}
}

func TestReplyAuthorizationAndStatus(t *testing.T) {
	uc, repo, _, _ := newTestUseCase()
	repo.ticket = &domain.Ticket{TicketNo: "AS1", Status: domain.TicketStatusOpen, RequesterUserID: 7}

	// A different user cannot reply on the user route.
	if _, err := uc.ReplyTicket(context.Background(), ReplyTicketRequest{TicketNo: "AS1", UserID: 99, Content: "x"}); err != domain.ErrTicketForbidden {
		t.Fatalf("expected forbidden, got %v", err)
	}
	// The requester can, as a user message.
	if _, err := uc.ReplyTicket(context.Background(), ReplyTicketRequest{TicketNo: "AS1", UserID: 7, Content: "x"}); err != nil {
		t.Fatalf("requester reply: %v", err)
	}
	if repo.replyCalls[len(repo.replyCalls)-1].Message.SenderType != domain.SenderTypeUser {
		t.Fatalf("expected user sender")
	}
	// A platform reply moves open -> processing.
	view, err := uc.ReplyTicket(context.Background(), ReplyTicketRequest{TicketNo: "AS1", UserID: 3, AsPlatform: true, Content: "y"})
	if err != nil {
		t.Fatalf("platform reply: %v", err)
	}
	if view.Ticket.Status != domain.TicketStatusProcessing {
		t.Fatalf("expected processing, got %s", view.Ticket.Status)
	}

	// Closed tickets reject replies.
	repo.ticket.Status = domain.TicketStatusClosed
	if _, err := uc.ReplyTicket(context.Background(), ReplyTicketRequest{TicketNo: "AS1", UserID: 7, Content: "z"}); err != domain.ErrTicketClosed {
		t.Fatalf("expected ErrTicketClosed, got %v", err)
	}
}

func TestRefundAndClose(t *testing.T) {
	uc, repo, refund, _ := newTestUseCase()

	// General tickets have no order to refund.
	repo.ticket = &domain.Ticket{TicketNo: "AS1", Status: domain.TicketStatusOpen, TicketType: domain.TicketTypeGeneral}
	if _, err := uc.RefundAndCloseTicket(context.Background(), RefundTicketRequest{TicketNo: "AS1"}); err != domain.ErrInvalidTicketRequest {
		t.Fatalf("expected invalid request, got %v", err)
	}

	// Order tickets refund via the port and close as refunded.
	repo.ticket = &domain.Ticket{
		TicketNo: "AS2", Status: domain.TicketStatusProcessing, TicketType: domain.TicketTypeOrder,
		Order: &domain.OrderSnapshot{OrderNo: "OR9", PayAmount: "6.8"},
	}
	view, err := uc.RefundAndCloseTicket(context.Background(), RefundTicketRequest{TicketNo: "AS2", OperatorUserID: 3, IdempotencyKey: "k"})
	if err != nil {
		t.Fatalf("refund: %v", err)
	}
	if refund.called == nil || refund.called.OrderNo != "OR9" {
		t.Fatalf("refund port not called with order: %+v", refund.called)
	}
	if view.Ticket.Status != domain.TicketStatusClosed || view.Ticket.Resolution == nil || view.Ticket.Resolution.Kind != domain.ResolutionRefunded {
		t.Fatalf("expected refunded close: %+v", view.Ticket)
	}
}

func TestAttachmentCountCap(t *testing.T) {
	uc, _, _, _ := newTestUseCase()
	many := make([]string, maxAttachments+1)
	for i := range many {
		many[i] = pngDataURL("x")
	}
	if _, err := uc.decodeAndUpload(context.Background(), "AS1", many); err != domain.ErrAttachmentInvalid {
		t.Fatalf("expected ErrAttachmentInvalid for too many attachments, got %v", err)
	}
}

func TestAttachmentSizeCap(t *testing.T) {
	uc, _, _, _ := newTestUseCase()
	big := pngDataURL(string(make([]byte, maxAttachmentBytes+1)))
	if _, err := uc.decodeAndUpload(context.Background(), "AS1", []string{big}); err != domain.ErrAttachmentTooLarge {
		t.Fatalf("expected ErrAttachmentTooLarge, got %v", err)
	}
}

func TestLoadAttachmentAuthorization(t *testing.T) {
	uc, repo, _, _ := newTestUseCase()
	repo.ticket = &domain.Ticket{TicketNo: "AS1", RequesterUserID: 7}
	repo.attachment = &domain.TicketAttachment{AttachmentNo: "AA1", TicketNo: "AS1", ObjectKey: "k", Mime: "image/png"}

	if _, _, err := uc.LoadAttachment(context.Background(), "AS1", "AA1", 99, false); err != domain.ErrTicketForbidden {
		t.Fatalf("expected forbidden for non-owner, got %v", err)
	}
	mime, content, err := uc.LoadAttachment(context.Background(), "AS1", "AA1", 7, false)
	if err != nil || mime != "image/png" || len(content) == 0 {
		t.Fatalf("owner load: mime=%q len=%d err=%v", mime, len(content), err)
	}
	// Admins can read any ticket's attachments.
	if _, _, err := uc.LoadAttachment(context.Background(), "AS1", "AA1", 99, true); err != nil {
		t.Fatalf("admin load: %v", err)
	}
}
