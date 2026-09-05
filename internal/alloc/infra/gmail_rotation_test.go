package infra

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/donnel666/remail/internal/alloc/domain"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func TestGmailCandidatesRotateAcrossDotAndPlusAllocations(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:gmail-rotation-%d?mode=memory&cache=shared", time.Now().UnixNano())), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	for _, statement := range []string{
		`CREATE TABLE users (id INTEGER PRIMARY KEY, status TEXT, role TEXT)`,
		`CREATE TABLE email_resources (id INTEGER PRIMARY KEY, type TEXT, owner_user_id INTEGER)`,
		`CREATE TABLE gmail_resources (id INTEGER PRIMARY KEY, email TEXT, status TEXT, for_sale BOOLEAN, alloc_bucket INTEGER, last_allocated_at DATETIME)`,
		`CREATE TABLE gmail_allocations (source TEXT, resource_id INTEGER, project_id INTEGER, mailbox TEXT, email TEXT)`,
		`INSERT INTO users VALUES (1, 'active', 'supplier')`,
		`INSERT INTO email_resources VALUES (1, 'gmail', 1), (2, 'gmail', 1)`,
		`INSERT INTO gmail_resources VALUES
			(1, 'ab@gmail.com', 'normal', TRUE, 1, NULL),
			(2, 'cd@gmail.com', 'normal', TRUE, 2, NULL)`,
	} {
		if err := db.Exec(statement).Error; err != nil {
			t.Fatalf("seed Gmail rotation: %v", err)
		}
	}

	repo := NewRepo(db)
	ctx := context.Background()
	list := func(mailbox domain.GmailMailbox) []uint {
		candidates, err := repo.ListGmailSourceCandidates(ctx, 10, 2, domain.SupplyScopePublic, mailbox, nil, 2)
		if err != nil {
			t.Fatalf("list %s candidates: %v", mailbox, err)
		}
		if len(candidates) != 2 {
			t.Fatalf("list %s candidates returned %d, want 2", mailbox, len(candidates))
		}
		return []uint{candidates[0].ResourceID, candidates[1].ResourceID}
	}

	if got := list(domain.GmailMailboxDot); got[0] != 1 || got[1] != 2 {
		t.Fatalf("initial dot candidates = %v, want [1 2]", got)
	}
	firstPage, err := repo.ListGmailSourceCandidates(ctx, 10, 2, domain.SupplyScopePublic, domain.GmailMailboxDot, nil, 1)
	if err != nil || len(firstPage) != 1 {
		t.Fatalf("list first Gmail candidate page = %v, %v", firstPage, err)
	}
	secondPage, err := repo.ListGmailSourceCandidates(ctx, 10, 2, domain.SupplyScopePublic, domain.GmailMailboxDot, &firstPage[0], 1)
	if err != nil || len(secondPage) != 1 || secondPage[0].ResourceID != 2 {
		t.Fatalf("list second Gmail candidate page = %v, %v; want resource 2", secondPage, err)
	}
	if err := repo.TouchGmailAllocated(ctx, 1, time.Now().UTC()); err != nil {
		t.Fatalf("touch dot allocation: %v", err)
	}
	if got := list(domain.GmailMailboxPlus); got[0] != 2 || got[1] != 1 {
		t.Fatalf("plus candidates after dot allocation = %v, want [2 1]", got)
	}
	if err := repo.TouchGmailAllocated(ctx, 2, time.Now().UTC().Add(time.Second)); err != nil {
		t.Fatalf("touch plus allocation: %v", err)
	}
	if got := list(domain.GmailMailboxDot); got[0] != 1 || got[1] != 2 {
		t.Fatalf("dot candidates after plus allocation = %v, want [1 2]", got)
	}
	firstPage, err = repo.ListGmailSourceCandidates(ctx, 10, 2, domain.SupplyScopePublic, domain.GmailMailboxDot, nil, 1)
	if err != nil || len(firstPage) != 1 {
		t.Fatalf("list first timestamped Gmail candidate page = %v, %v", firstPage, err)
	}
	secondPage, err = repo.ListGmailSourceCandidates(ctx, 10, 2, domain.SupplyScopePublic, domain.GmailMailboxDot, &firstPage[0], 1)
	if err != nil || len(secondPage) != 1 || secondPage[0].ResourceID != 2 {
		t.Fatalf("list second timestamped Gmail candidate page = %v, %v; want resource 2", secondPage, err)
	}

	if err := db.Exec(`INSERT INTO gmail_allocations VALUES ('local', 1, 10, 'dot', 'a.b@gmail.com')`).Error; err != nil {
		t.Fatalf("seed first Gmail dot alias: %v", err)
	}
	if got := list(domain.GmailMailboxDot); got[0] != 1 || got[1] != 2 {
		t.Fatalf("dot candidates with Googlemail variants remaining = %v, want [1 2]", got)
	}
	if err := db.Exec(`INSERT INTO gmail_allocations VALUES
		('local', 1, 10, 'dot', 'ab@googlemail.com'),
		('local', 1, 10, 'dot', 'a.b@googlemail.com')`).Error; err != nil {
		t.Fatalf("exhaust first Gmail dot aliases: %v", err)
	}
	candidates, err := repo.ListGmailSourceCandidates(ctx, 10, 2, domain.SupplyScopePublic, domain.GmailMailboxDot, nil, 1)
	if err != nil {
		t.Fatalf("list candidates after first resource exhaustion: %v", err)
	}
	if len(candidates) != 1 || candidates[0].ResourceID != 2 {
		t.Fatalf("candidate after first resource exhaustion = %v, want resource 2", candidates)
	}
}
