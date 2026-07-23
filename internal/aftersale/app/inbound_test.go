package app

import (
	"context"
	"testing"

	"github.com/donnel666/remail/internal/aftersale/domain"
)

func inboundTestUseCase() (*UseCase, *fakeRepo) {
	uc, repo, _, _ := newTestUseCase()
	uc.mailConfig = TicketMailConfig{ReplyLocalPart: "support", ReplyDomain: "tickets.example.com", ReplySecret: "secret"}
	return uc, repo
}

func TestParseReplyRecipient(t *testing.T) {
	uc, _ := inboundTestUseCase()
	cases := []struct {
		recipient       string
		wantNo, wantTok string
		ok              bool
	}{
		{"support+as1b2c3-a1b2c3d4@tickets.example.com", "AS1B2C3", "a1b2c3d4", true},
		{"SUPPORT+as1-tok@tickets.example.com", "AS1", "tok", true}, // prefix case-insensitive
		{"other+as1-tok@tickets.example.com", "", "", false},        // wrong local part
		{"support-as1-tok@tickets.example.com", "", "", false},      // no plus
		{"support+as1@tickets.example.com", "", "", false},          // no token separator
		{"support+as1-@tickets.example.com", "", "", false},         // empty token
		{"not-an-address", "", "", false},
	}
	for _, c := range cases {
		no, tok, ok := uc.parseReplyRecipient(c.recipient)
		if ok != c.ok || no != c.wantNo || tok != c.wantTok {
			t.Errorf("%q -> (%q,%q,%v) want (%q,%q,%v)", c.recipient, no, tok, ok, c.wantNo, c.wantTok, c.ok)
		}
	}
}

func TestStripQuotedReply(t *testing.T) {
	delimited := "这是我的回复。\n谢谢。\n\n" + replyDelimiter + "\n工单号：AS1\n> 您好，\n> 客服回复了您的工单"
	if got := stripQuotedReply(delimited); got != "这是我的回复。\n谢谢。" {
		t.Fatalf("delimiter strip = %q", got)
	}

	gmail := "My new reply.\n\nOn Mon, Jul 17, 2026 at 10:00 AM support wrote:\n> previous content\n> more"
	if got := stripQuotedReply(gmail); got != "My new reply." {
		t.Fatalf("quote-header strip = %q", got)
	}

	chinese := "新的回复内容\n\n在 2026年7月17日, 客服 写道：\n> 引用"
	if got := stripQuotedReply(chinese); got != "新的回复内容" {
		t.Fatalf("chinese quote strip = %q", got)
	}

	if got := stripQuotedReply("  仅一行  "); got != "仅一行" {
		t.Fatalf("plain trim = %q", got)
	}
}

func TestIngestInboundReply(t *testing.T) {
	uc, repo := inboundTestUseCase()
	uc.SetOwnerLookupPort(fakeOwners{admins: []RequesterSummary{{
		ID: 42, Email: "admin@example.com", Nickname: "Admin", Role: "super_admin", Enabled: true,
	}}})
	mailer := &fakeMail{}
	uc.mail = mailer
	repo.ticket = &domain.Ticket{TicketNo: "AS1", ReplyToken: "tok", RequesterUserID: 7, Status: domain.TicketStatusProcessing}

	body := "客户的邮件回复\n\n" + replyDelimiter + "\n> 原文"
	err := uc.IngestInboundReply(context.Background(), InboundReplyCommand{
		Recipient: "support+as1-tok@tickets.example.com",
		Body:      body,
	})
	if err != nil {
		t.Fatalf("ingest: %v", err)
	}
	if len(repo.replyCalls) != 1 {
		t.Fatalf("expected 1 reply, got %d", len(repo.replyCalls))
	}
	msg := repo.replyCalls[0].Message
	if msg.SenderType != domain.SenderTypeUser || msg.Content != "客户的邮件回复" || msg.SenderUserID != 7 || msg.SenderName != "nick" {
		t.Fatalf("unexpected message: %+v", msg)
	}
	if len(mailer.sent) != 1 || mailer.sent[0].To != "admin@example.com" {
		t.Fatalf("super-admin notifications: %+v", mailer.sent)
	}
	expectedReplyTo := uc.mailConfig.replyAddress("AS1", uc.mailConfig.platformReplyToken("AS1", "tok", 42))
	if mailer.sent[0].ReplyTo != expectedReplyTo {
		t.Fatalf("super-admin Reply-To = %q", mailer.sent[0].ReplyTo)
	}

	// Bad token is rejected silently (no reply, no error).
	repo.replyCalls = nil
	if err := uc.IngestInboundReply(context.Background(), InboundReplyCommand{
		Recipient: "support+as1-wrong@tickets.example.com", Body: "hi",
	}); err != nil || len(repo.replyCalls) != 0 {
		t.Fatalf("bad token: err=%v replies=%d", err, len(repo.replyCalls))
	}

	// Closed ticket drops the reply.
	repo.ticket.Status = domain.TicketStatusClosed
	if err := uc.IngestInboundReply(context.Background(), InboundReplyCommand{
		Recipient: "support+as1-tok@tickets.example.com", Body: "hi",
	}); err != nil || len(repo.replyCalls) != 0 {
		t.Fatalf("closed ticket: err=%v replies=%d", err, len(repo.replyCalls))
	}

	// Empty body after stripping drops the reply.
	repo.ticket.Status = domain.TicketStatusOpen
	if err := uc.IngestInboundReply(context.Background(), InboundReplyCommand{
		Recipient: "support+as1-tok@tickets.example.com", Body: replyDelimiter + "\n> quoted only",
	}); err != nil || len(repo.replyCalls) != 0 {
		t.Fatalf("empty body: err=%v replies=%d", err, len(repo.replyCalls))
	}
}

func TestIngestInboundSuperAdminReplyNotifiesRequester(t *testing.T) {
	uc, repo := inboundTestUseCase()
	uc.SetOwnerLookupPort(fakeOwners{admins: []RequesterSummary{{
		ID: 42, Email: "admin@example.com", Nickname: "Admin", Role: "super_admin", Enabled: true,
	}}})
	mailer := &fakeMail{}
	uc.mail = mailer
	repo.ticket = &domain.Ticket{
		TicketNo: "AS1", Title: "help", ReplyToken: "tok", RequesterUserID: 7, Status: domain.TicketStatusOpen,
	}
	adminToken := uc.mailConfig.platformReplyToken("AS1", "tok", 42)

	err := uc.IngestInboundReply(context.Background(), InboundReplyCommand{
		Recipient: "support+as1-" + adminToken + "@tickets.example.com",
		Body:      "管理员的邮件回复",
	})
	if err != nil {
		t.Fatalf("ingest: %v", err)
	}
	if len(repo.replyCalls) != 1 {
		t.Fatalf("expected 1 reply, got %d", len(repo.replyCalls))
	}
	msg := repo.replyCalls[0].Message
	if msg.SenderType != domain.SenderTypePlatform || msg.SenderUserID != 42 || msg.SenderName != "Admin" {
		t.Fatalf("unexpected platform message: %+v", msg)
	}
	if len(mailer.sent) != 1 || mailer.sent[0].To != "u@example.com" {
		t.Fatalf("requester notifications: %+v", mailer.sent)
	}
	if mailer.sent[0].ReplyTo != uc.mailConfig.replyAddress("AS1", "tok") {
		t.Fatalf("requester Reply-To = %q", mailer.sent[0].ReplyTo)
	}
}

func TestReplyAddressAndToken(t *testing.T) {
	config := TicketMailConfig{ReplyLocalPart: "support", ReplyDomain: "tickets.example.com", ReplySecret: "secret"}
	if got := config.replyAddress("AS1B2C3", "tok123"); got != "support+AS1B2C3-tok123@tickets.example.com" {
		t.Fatalf("replyAddress = %q", got)
	}
	if !config.enabled() {
		t.Fatal("config should be enabled")
	}
	adminToken := config.platformReplyToken("AS1B2C3", "tok123", 42)
	if len(adminToken) != 16 || adminToken == "tok123" || adminToken != config.platformReplyToken("AS1B2C3", "tok123", 42) {
		t.Fatalf("unexpected platform token %q", adminToken)
	}
	if (TicketMailConfig{}).enabled() {
		t.Fatal("empty config should be disabled")
	}
	if token := newReplyToken(); len(token) != 16 {
		t.Fatalf("token length = %d (%q)", len(token), token)
	}
}
