package gmail

import (
	"context"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func TestProbeICloudDeliveryUsesLinkedGmailIMAPAndToken(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:gmail-icloud-probe?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	if err := db.AutoMigrate(&localResourceModel{}); err != nil {
		t.Fatalf("migrate database: %v", err)
	}
	if err := db.Create(&localResourceModel{
		ID: 9, ResourceType: "gmail", Email: "target@gmail.com", AppPassword: "app-password", Status: LocalResourceNormal,
	}).Error; err != nil {
		t.Fatalf("create Gmail resource: %v", err)
	}
	service := NewService(db, nil)
	page := 0
	service.fetch = func(_ context.Context, email, appPassword string, cursors localGmailFolderCursors, _ time.Time, _ bool) ([]localGmailFetchedMessage, localGmailFolderCursors, error) {
		if email != "target@gmail.com" || appPassword != "app-password" {
			t.Fatalf("credentials did not stay inside Gmail: email=%q appPassword=%q", email, appPassword)
		}
		page++
		if cursors.Inbox < 10 {
			return nil, localGmailFolderCursors{Inbox: cursors.Inbox + 1}, nil
		}
		return []localGmailFetchedMessage{{
			Recipient: "target@gmail.com", Raw: []byte("To: alias@icloud.com\r\nSubject: probe\r\n\r\nremail-icloud-probe-9-1"),
		}}, cursors, nil
	}
	found, err := service.ProbeICloudDelivery(context.Background(), 9, "alias@icloud.com", "remail-icloud-probe-9-1", time.Now().Add(-time.Minute))
	if err != nil || !found {
		t.Fatalf("probe delivery: found=%v err=%v", found, err)
	}
	if page != 11 {
		t.Fatalf("probe stopped before the delivery page: pages=%d", page)
	}
	found, err = service.ProbeICloudDelivery(context.Background(), 9, "other@icloud.com", "remail-icloud-probe-9-1", time.Now().Add(-time.Minute))
	if err != nil || found {
		t.Fatalf("recipient mismatch must not pass: found=%v err=%v", found, err)
	}
}
