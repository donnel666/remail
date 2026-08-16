package kitesim

import (
	"database/sql"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func TestJSONTextNullClearsStoredValue(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&operationModel{}); err != nil {
		t.Fatal(err)
	}
	operation := operationModel{SecretPayload: jsonText(`{"cvc":"123"}`)}
	if err := db.Create(&operation).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&operationModel{}).Where("id = ?", operation.ID).Update("secret_payload", nil).Error; err != nil {
		t.Fatal(err)
	}
	var raw sql.NullString
	if err := db.Raw("SELECT secret_payload FROM kitesim_operations WHERE id = ?", operation.ID).Scan(&raw).Error; err != nil {
		t.Fatal(err)
	}
	if raw.Valid {
		t.Fatalf("database secret payload = %q, want NULL", raw.String)
	}
	if err := db.First(&operation, operation.ID).Error; err != nil {
		t.Fatal(err)
	}
	if len(operation.SecretPayload) != 0 {
		t.Fatalf("scanned secret payload = %q, want empty", operation.SecretPayload)
	}
}
