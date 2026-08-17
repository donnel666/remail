package icloud

import (
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func TestICloudJSONNullClearsReusedModel(t *testing.T) {
	type row struct {
		ID      uint       `gorm:"primaryKey"`
		Secret  iCloudJSON `gorm:"type:json;serializer:json"`
		Session iCloudJSON `gorm:"type:json;serializer:json"`
	}

	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&row{}); err != nil {
		t.Fatal(err)
	}
	stored := row{Secret: []byte(`{"password":"secret"}`), Session: []byte(`{"session":true}`)}
	if err := db.Create(&stored).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&row{}).Where("id = ?", stored.ID).Updates(map[string]any{"secret": nil, "session": nil}).Error; err != nil {
		t.Fatal(err)
	}
	var secretIsNull, sessionIsNull bool
	if err := db.Raw("SELECT secret IS NULL, session IS NULL FROM rows WHERE id = ?", stored.ID).Row().Scan(&secretIsNull, &sessionIsNull); err != nil {
		t.Fatal(err)
	}
	if !secretIsNull || !sessionIsNull {
		t.Fatalf("database values were not cleared: secret=%v session=%v", secretIsNull, sessionIsNull)
	}
	if err := db.First(&stored, stored.ID).Error; err != nil {
		t.Fatal(err)
	}
	if len(stored.Secret) != 0 || len(stored.Session) != 0 {
		t.Fatalf("reused model retained JSON values: secret=%q session=%q", stored.Secret, stored.Session)
	}
}
