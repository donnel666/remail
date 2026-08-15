package icloud

import (
	"context"
	"errors"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	coreDomain "github.com/donnel666/remail/internal/core/domain"
	governancedomain "github.com/donnel666/remail/internal/governance/domain"
	governanceinfra "github.com/donnel666/remail/internal/governance/infra"
	mailapp "github.com/donnel666/remail/internal/mailtransport/app"
	"github.com/donnel666/remail/internal/systemsettings/runtimeconfig"
	"github.com/glebarez/sqlite"
	"github.com/hibiken/asynq"
	"gorm.io/gorm"
)

type iCloudPreparationDomainTestModel struct {
	ID               uint   `gorm:"column:id;primaryKey"`
	Domain           string `gorm:"column:domain"`
	Purpose          string `gorm:"column:purpose"`
	Status           string `gorm:"column:status"`
	AllowNewBindings bool   `gorm:"column:allow_new_bindings"`
}

func (iCloudPreparationDomainTestModel) TableName() string { return "domain_resources" }

type iCloudPreparationMailTestModel struct {
	ID               uint       `gorm:"column:id;primaryKey"`
	HeaderFrom       string     `gorm:"column:header_from"`
	Recipient        string     `gorm:"column:recipient"`
	Subject          string     `gorm:"column:subject"`
	BodyPreview      string     `gorm:"column:body_preview"`
	VerificationCode string     `gorm:"column:verification_code"`
	MessageIDHeader  string     `gorm:"column:message_id_header"`
	SourceObjectKey  string     `gorm:"column:source_object_key"`
	ReceivedAt       *time.Time `gorm:"column:received_at"`
	ParsedAt         *time.Time `gorm:"column:parsed_at"`
	CreatedAt        time.Time  `gorm:"column:created_at"`
	UpdatedAt        time.Time  `gorm:"column:updated_at"`
}

func (iCloudPreparationMailTestModel) TableName() string { return "inbound_mails" }

func TestCreateAdminICloudImportPreparationUsesEligibleConfiguredDomain(t *testing.T) {
	db := newICloudPreparationTestDB(t, "icloud-preparation-domain", &iCloudPreparationDomainTestModel{}, &iCloudImportPreparationModel{}, &iCloudImportModel{})
	domains := []iCloudPreparationDomainTestModel{
		{ID: 1, Domain: "relay.example", Purpose: "binding", Status: "normal", AllowNewBindings: true},
		{ID: 2, Domain: "disabled.example", Purpose: "binding", Status: "normal", AllowNewBindings: false},
		{ID: 3, Domain: "other.example", Purpose: "binding", Status: "normal", AllowNewBindings: true},
	}
	if err := db.Create(&domains).Error; err != nil {
		t.Fatalf("create domains: %v", err)
	}
	setICloudPreparationRuntimeValue(t, runtimeconfig.ICloudForwardingSuffixesKey, "relay.example,disabled.example")

	now := time.Date(2026, 8, 15, 8, 0, 0, 0, time.UTC)
	oldReferencedID := uint(12)
	preparations := []iCloudImportPreparationModel{
		{ID: 10, OperatorUserID: 9, DomainResourceID: 1, ForwardToEmail: "old@relay.example", ExpiresAt: now.Add(-25 * time.Hour), CreatedAt: now.Add(-26 * time.Hour), UpdatedAt: now},
		{ID: 11, OperatorUserID: 9, DomainResourceID: 1, ForwardToEmail: "recent@relay.example", ExpiresAt: now.Add(-23 * time.Hour), CreatedAt: now.Add(-24 * time.Hour), UpdatedAt: now},
		{ID: oldReferencedID, OperatorUserID: 9, DomainResourceID: 1, ForwardToEmail: "referenced@relay.example", ExpiresAt: now.Add(-25 * time.Hour), CreatedAt: now.Add(-26 * time.Hour), UpdatedAt: now},
	}
	if err := db.Create(&preparations).Error; err != nil {
		t.Fatalf("create cleanup preparations: %v", err)
	}
	if err := db.Create(&iCloudImportModel{ID: 20, PreparationID: &oldReferencedID}).Error; err != nil {
		t.Fatalf("create referenced import: %v", err)
	}
	service := NewService(db, nil, nil)
	service.now = func() time.Time { return now }
	result, err := service.CreateAdminICloudImportPreparation(context.Background(), 9)
	if err != nil {
		t.Fatalf("create preparation: %v", err)
	}
	local, domain, found := strings.Cut(result.ForwardToEmail, "@")
	if result.Status != "waiting" || !found ||
		!regexp.MustCompile(`^[a-z]{6,18}[0-9]{5}$`).MatchString(local) || domain != "relay.example" {
		t.Fatalf("unexpected preparation: %#v", result)
	}
	var stored iCloudImportPreparationModel
	if err := db.First(&stored, result.ID).Error; err != nil || stored.DomainResourceID != 1 {
		t.Fatalf("stored preparation=%#v err=%v", stored, err)
	}
	var remaining []uint
	if err := db.Model(&iCloudImportPreparationModel{}).Order("id").Pluck("id", &remaining).Error; err != nil {
		t.Fatalf("list preparations after cleanup: %v", err)
	}
	if slices.Contains(remaining, 10) || !slices.Contains(remaining, 11) || !slices.Contains(remaining, oldReferencedID) {
		t.Fatalf("unexpected cleanup result: %v", remaining)
	}
}

func TestGeneratedICloudImportAddressesDoNotShadowAppleVerificationCode(t *testing.T) {
	setICloudPreparationRuntimeValue(t, "verification_code_pattern", `["(?:^|[^\\d])(\\d{6,8})(?:[^\\d]|$)"]`)
	now := time.Date(2026, 8, 15, 20, 0, 0, 0, time.UTC)
	for iteration := 0; iteration < 10_000; iteration++ {
		local, err := generateICloudImportPreparationLocal()
		if err != nil {
			t.Fatalf("generate address %d: %v", iteration, err)
		}
		address := local + "@aishop6.com"
		raw := []byte("From: Apple <noreply@apple.com>\r\n" +
			"To: " + address + "\r\n" +
			"Subject: 验证你的 Apple 账户电子邮件地址\r\n" +
			"Content-Type: text/plain; charset=utf-8\r\n\r\n" +
			"你最近已添加 " + address + " 作为你 Apple 账户的额外电子邮件地址。" +
			"为验证此电子邮件地址属于你，请在你的电子邮件验证页面输入下方验证码：\r\n\r\n" +
			"895089\r\n")
		if code := mailapp.ParseInboundMessageSummary(raw, now).VerificationCode; code != "895089" {
			t.Fatalf("iteration %d address %q extracted %q", iteration, address, code)
		}
	}
}

func TestGetAdminICloudImportPreparationAcceptsOnlyNewExactAppleMail(t *testing.T) {
	db := newICloudPreparationTestDB(t, "icloud-preparation-mail", &iCloudImportPreparationModel{}, &iCloudPreparationMailTestModel{})
	base := time.Date(2026, 8, 15, 9, 0, 0, 0, time.UTC)
	preparation := iCloudImportPreparationModel{
		ID: 7, OperatorUserID: 9, DomainResourceID: 1,
		ForwardToEmail: "icloud_test@relay.example", ExpiresAt: base.Add(30 * time.Minute),
		CreatedAt: base, UpdatedAt: base,
	}
	if err := db.Create(&preparation).Error; err != nil {
		t.Fatalf("create preparation: %v", err)
	}
	files := &icloudImportFileStore{files: map[string]governancedomain.PrivateFile{}}
	storePreparationMail := func(id uint, recipient, sender, code string, createdAt time.Time) {
		t.Helper()
		parsedAt := createdAt
		if err := db.Create(&iCloudPreparationMailTestModel{
			ID: id, HeaderFrom: sender, Recipient: recipient, VerificationCode: code,
			ParsedAt: &parsedAt, CreatedAt: createdAt, UpdatedAt: createdAt,
		}).Error; err != nil {
			t.Fatalf("create inbound mail: %v", err)
		}
	}
	storePreparationMail(1, preparation.ForwardToEmail, "noreply@apple.com", "111111", base.Add(-time.Second))
	storePreparationMail(2, "other@relay.example", "noreply@apple.com", "222222", base.Add(time.Minute))
	storePreparationMail(3, preparation.ForwardToEmail, "other@example.com", "333333", base.Add(3*time.Minute))
	objectKey := "mail/apple.eml"
	files.files[objectKey] = governancedomain.PrivateFile{ObjectKey: objectKey, ContentBytes: []byte(
		"From: Apple <noreply@apple.com>\r\nSubject: Apple verification\r\n\r\nVerification code 088556",
	)}
	if err := db.Create(&iCloudPreparationMailTestModel{
		ID: 4, Recipient: preparation.ForwardToEmail, SourceObjectKey: objectKey,
		CreatedAt: base.Add(2 * time.Minute), UpdatedAt: base.Add(2 * time.Minute),
	}).Error; err != nil {
		t.Fatalf("create unparsed Apple mail: %v", err)
	}

	service := NewService(db, nil, files)
	service.now = func() time.Time { return base.Add(4 * time.Minute) }
	result, err := service.GetAdminICloudImportPreparation(context.Background(), 9, preparation.ID)
	if err != nil {
		t.Fatalf("get preparation: %v", err)
	}
	if result.Status != "code_received" || result.VerificationCode != "088556" {
		t.Fatalf("unexpected preparation result: %#v", result)
	}
	var parsed iCloudPreparationMailTestModel
	if err := db.First(&parsed, 4).Error; err != nil || parsed.ParsedAt == nil || parsed.VerificationCode != "088556" {
		t.Fatalf("stored parsed mail=%#v err=%v", parsed, err)
	}
	if _, err := service.GetAdminICloudImportPreparation(context.Background(), 10, preparation.ID); !errors.Is(err, ErrICloudImportPreparationNotFound) {
		t.Fatalf("cross-operator read error = %v", err)
	}
}

func TestPreparedICloudImportConsumesVerifiedPreparationOnce(t *testing.T) {
	redisServer := miniredis.RunT(t)
	queue := asynq.NewClient(asynq.RedisClientOpt{Addr: redisServer.Addr()})
	t.Cleanup(func() { _ = queue.Close() })
	db := newICloudPreparationTestDB(
		t,
		"icloud-preparation-import",
		&iCloudImportPreparationModel{},
		&iCloudPreparationDomainTestModel{},
		&iCloudImportModel{},
		&iCloudImportItemModel{},
		&iCloudRootModel{},
		&iCloudResourceModel{},
		&iCloudResourceChannelModel{},
		&iCloudAliasModel{},
		&governanceinfra.OperationLogModel{},
	)
	now := time.Date(2026, 8, 15, 10, 0, 0, 0, time.UTC)
	verifiedAt := now.Add(-time.Minute)
	domains := []iCloudPreparationDomainTestModel{
		{ID: 1, Domain: "relay.example", Purpose: "binding", Status: "normal", AllowNewBindings: true},
		{ID: 2, Domain: "purpose.example", Purpose: "not_sale", Status: "normal", AllowNewBindings: true},
		{ID: 3, Domain: "status.example", Purpose: "binding", Status: "disabled", AllowNewBindings: true},
		{ID: 4, Domain: "bindings.example", Purpose: "binding", Status: "normal", AllowNewBindings: false},
		{ID: 5, Domain: "whitelist.example", Purpose: "binding", Status: "normal", AllowNewBindings: true},
	}
	if err := db.Create(&domains).Error; err != nil {
		t.Fatalf("create import domains: %v", err)
	}
	setICloudPreparationRuntimeValue(t, runtimeconfig.ICloudForwardingSuffixesKey, "relay.example,purpose.example,status.example,bindings.example")
	preparations := []iCloudImportPreparationModel{
		{ID: 1, OperatorUserID: 9, DomainResourceID: 1, ForwardToEmail: "icloud_ok@relay.example", VerificationCode: "088556", VerifiedAt: &verifiedAt, ExpiresAt: now.Add(20 * time.Minute), CreatedAt: now.Add(-2 * time.Minute), UpdatedAt: now},
		{ID: 2, OperatorUserID: 9, DomainResourceID: 1, ForwardToEmail: "icloud_waiting@relay.example", ExpiresAt: now.Add(20 * time.Minute), CreatedAt: now, UpdatedAt: now},
		{ID: 3, OperatorUserID: 9, DomainResourceID: 1, ForwardToEmail: "icloud_expired@relay.example", VerificationCode: "123456", VerifiedAt: &verifiedAt, ExpiresAt: now.Add(-time.Second), CreatedAt: now.Add(-time.Hour), UpdatedAt: now},
		{ID: 4, OperatorUserID: 9, DomainResourceID: 2, ForwardToEmail: "icloud_purpose@purpose.example", VerificationCode: "123456", VerifiedAt: &verifiedAt, ExpiresAt: now.Add(time.Hour), CreatedAt: now, UpdatedAt: now},
		{ID: 5, OperatorUserID: 9, DomainResourceID: 3, ForwardToEmail: "icloud_status@status.example", VerificationCode: "123456", VerifiedAt: &verifiedAt, ExpiresAt: now.Add(time.Hour), CreatedAt: now, UpdatedAt: now},
		{ID: 6, OperatorUserID: 9, DomainResourceID: 4, ForwardToEmail: "icloud_bindings@bindings.example", VerificationCode: "123456", VerifiedAt: &verifiedAt, ExpiresAt: now.Add(time.Hour), CreatedAt: now, UpdatedAt: now},
		{ID: 7, OperatorUserID: 9, DomainResourceID: 5, ForwardToEmail: "icloud_whitelist@whitelist.example", VerificationCode: "123456", VerifiedAt: &verifiedAt, ExpiresAt: now.Add(time.Hour), CreatedAt: now, UpdatedAt: now},
		{ID: 8, OperatorUserID: 9, DomainResourceID: 1, ForwardToEmail: "icloud_mismatch@purpose.example", VerificationCode: "123456", VerifiedAt: &verifiedAt, ExpiresAt: now.Add(time.Hour), CreatedAt: now, UpdatedAt: now},
	}
	if err := db.Create(&preparations).Error; err != nil {
		t.Fatalf("create preparations: %v", err)
	}
	service := NewService(db, queue, &icloudImportFileStore{})
	service.now = func() time.Time { return now }
	service.SetImportOwnerValidator(func(context.Context, uint) (bool, error) { return true, nil })
	content := []byte("main@icloud.com----" + testICloudOldCurl)

	for _, preparationID := range []uint{2, 3} {
		_, _, err := service.AcceptAdminICloudPreparedTXTFile(
			context.Background(), 9, 7, preparationID, "icloud.txt", content,
			coreDomain.ImportErrorStrategySkip, now.Add(time.Hour), "invalid-preparation-"+string(rune('0'+preparationID)),
			"request-invalid", "/v1/admin/icloud/resources/imports",
		)
		if !errors.Is(err, ErrICloudImportPreparationConflict) {
			t.Fatalf("preparation %d error = %v", preparationID, err)
		}
	}
	for _, preparationID := range []uint{4, 5, 6, 7, 8} {
		_, _, err := service.AcceptAdminICloudPreparedTXTFile(
			context.Background(), 9, 7, preparationID, "icloud.txt", content,
			coreDomain.ImportErrorStrategySkip, now.Add(time.Hour), "ineligible-domain-"+string(rune('0'+preparationID)),
			"request-domain", "/v1/admin/icloud/resources/imports",
		)
		if !errors.Is(err, ErrICloudImportPreparationConflict) {
			t.Fatalf("ineligible preparation %d error = %v", preparationID, err)
		}
	}
	if _, _, err := service.AcceptAdminICloudPreparedTXTFile(
		context.Background(), 9, 7, 1, "icloud.txt", append(content, append([]byte("\nmain2@icloud.com----"), []byte(testICloudOldCurl)...)...),
		coreDomain.ImportErrorStrategySkip, now.Add(time.Hour), "multiple-lines", "request-lines", "/v1/admin/icloud/resources/imports",
	); !errors.Is(err, ErrICloudImportInvalid) {
		t.Fatalf("multiple-line import error = %v", err)
	}

	result, reused, err := service.AcceptAdminICloudPreparedTXTFile(
		context.Background(), 9, 7, 1, "icloud.txt", content,
		coreDomain.ImportErrorStrategySkip, now.Add(time.Hour), "prepared-import", "request-ok", "/v1/admin/icloud/resources/imports",
	)
	if err != nil || reused || result == nil {
		t.Fatalf("accept prepared import: result=%#v reused=%v err=%v", result, reused, err)
	}
	var storedImport iCloudImportModel
	if err := db.First(&storedImport, result.ImportID).Error; err != nil || storedImport.PreparationID == nil || *storedImport.PreparationID != 1 || storedImport.ForwardToEmail != "icloud_ok@relay.example" {
		t.Fatalf("stored import=%#v err=%v", storedImport, err)
	}
	var storedPreparation iCloudImportPreparationModel
	if err := db.First(&storedPreparation, 1).Error; err != nil || storedPreparation.ConsumedAt == nil {
		t.Fatalf("stored preparation=%#v err=%v", storedPreparation, err)
	}
	if err := service.ProcessICloudImport(context.Background(), iCloudImportTask{ImportID: storedImport.ID, Generation: storedImport.Generation}); err != nil {
		t.Fatalf("process prepared import: %v", err)
	}
	var resource iCloudResourceModel
	if err := db.First(&resource).Error; err != nil || resource.SelectedForwardTo != "icloud_ok@relay.example" || resource.RequiredForwardTo != "icloud_ok@relay.example" {
		t.Fatalf("prepared resource=%#v err=%v", resource, err)
	}
	if _, _, err := service.AcceptAdminICloudPreparedTXTFile(
		context.Background(), 9, 7, 1, "icloud.txt", []byte("other@icloud.com----"+testICloudOldCurl),
		coreDomain.ImportErrorStrategySkip, now.Add(time.Hour), "prepared-import-other", "request-other", "/v1/admin/icloud/resources/imports",
	); !errors.Is(err, ErrICloudImportConflict) {
		t.Fatalf("reused preparation error = %v", err)
	}
}

func newICloudPreparationTestDB(t *testing.T, name string, models ...any) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+name+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	if err := db.AutoMigrate(models...); err != nil {
		t.Fatalf("migrate database: %v", err)
	}
	return db
}

func setICloudPreparationRuntimeValue(t *testing.T, key, value string) {
	t.Helper()
	previous := runtimeconfig.String(key, "")
	runtimeconfig.Set(key, value)
	t.Cleanup(func() {
		if previous == "" {
			runtimeconfig.Delete(key)
			return
		}
		runtimeconfig.Set(key, previous)
	})
}
