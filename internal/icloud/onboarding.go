package icloud

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/mail"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	governancedomain "github.com/donnel666/remail/internal/governance/domain"
	"github.com/donnel666/remail/internal/platform"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	iCloudOnboardingMaxLines    = 1000
	iCloudOnboardingMaxAttempts = 5

	iCloudOnboardingProcessing = "processing"
	iCloudOnboardingWaiting    = "waiting"
	iCloudOnboardingCompleted  = "completed"
	iCloudOnboardingFailed     = "failed"
)

var (
	ErrICloudOnboardingInvalid   = errors.New("icloud: invalid account onboarding import")
	ErrICloudOnboardingConflict  = errors.New("icloud: account onboarding idempotency conflict")
	ErrICloudOnboardingNotFound  = errors.New("icloud: account onboarding import not found")
	ErrICloudOnboardingTemporary = errors.New("icloud: account onboarding temporarily unavailable")
)

type iCloudOnboardingImportModel struct {
	ID                 uint                        `gorm:"column:id;primaryKey;autoIncrement"`
	OwnerUserID        uint                        `gorm:"column:owner_user_id"`
	OperatorUserID     uint                        `gorm:"column:operator_user_id"`
	Status             string                      `gorm:"column:status"`
	AcceptedCount      int                         `gorm:"column:accepted_count"`
	CompletedCount     int                         `gorm:"column:completed_count"`
	FailedCount        int                         `gorm:"column:failed_count"`
	WaitingCount       int                         `gorm:"column:waiting_count"`
	ResourceExpireAt   time.Time                   `gorm:"column:resource_expire_at"`
	LastSafeError      string                      `gorm:"column:last_safe_error"`
	RequestID          string                      `gorm:"column:request_id"`
	Path               string                      `gorm:"column:path"`
	IdempotencyKey     string                      `gorm:"column:idempotency_key"`
	RequestFingerprint string                      `gorm:"column:request_fingerprint"`
	CreatedAt          time.Time                   `gorm:"column:created_at"`
	UpdatedAt          time.Time                   `gorm:"column:updated_at"`
	Tasks              []iCloudOnboardingTaskModel `gorm:"foreignKey:ImportID"`
}

func (iCloudOnboardingImportModel) TableName() string { return "icloud_account_onboarding_imports" }

type iCloudOnboardingTaskModel struct {
	ID                          uint       `gorm:"column:id;primaryKey;autoIncrement"`
	ImportID                    *uint      `gorm:"column:import_id"`
	ResourceID                  *uint      `gorm:"column:resource_id"`
	TaskKind                    string     `gorm:"column:task_kind"`
	LineNumber                  int        `gorm:"column:line_number"`
	PrimaryEmail                string     `gorm:"column:primary_email"`
	AccountRole                 string     `gorm:"column:account_role"`
	FamilyPrimaryResourceID     *uint      `gorm:"column:family_primary_resource_id"`
	FamilyReservationConfirmed  bool       `gorm:"column:family_reservation_confirmed"`
	Region                      string     `gorm:"column:region"`
	CountryCode                 string     `gorm:"column:country_code"`
	ICloudOpened                bool       `gorm:"column:icloud_opened"`
	FamilyInviteURL             string     `gorm:"column:family_invite_url"`
	BoundPhoneNumber            string     `gorm:"column:bound_phone_number"`
	BoundPhoneCountryCode       string     `gorm:"column:bound_phone_country_code"`
	BoundPhoneSource            string     `gorm:"column:bound_phone_source"`
	KitesimPhoneID              *uint      `gorm:"column:kitesim_phone_id"`
	SecretPayload               iCloudJSON `gorm:"column:secret_payload;type:json"`
	SessionPayload              iCloudJSON `gorm:"column:session_payload;type:json"`
	ManualVerificationCode      string     `gorm:"column:manual_verification_code"`
	PendingSMSPurpose           string     `gorm:"column:pending_sms_purpose"`
	SMSSentAt                   *time.Time `gorm:"column:sms_sent_at"`
	SMSPollDeadline             *time.Time `gorm:"column:sms_poll_deadline"`
	ForwardPreparationID        *uint      `gorm:"column:forward_preparation_id"`
	Status                      string     `gorm:"column:status"`
	Stage                       string     `gorm:"column:stage"`
	DispatchStatus              string     `gorm:"column:dispatch_status"`
	Generation                  uint64     `gorm:"column:generation"`
	ExpectedCredentialRevision  uint64     `gorm:"column:expected_credential_revision"`
	ClaimToken                  string     `gorm:"column:claim_token"`
	Attempts                    int        `gorm:"column:attempts"`
	MaxAttempts                 int        `gorm:"column:max_attempts"`
	StageAttempts               int        `gorm:"column:stage_attempts"`
	NextAttemptAt               *time.Time `gorm:"column:next_attempt_at"`
	LastErrorCategory           string     `gorm:"column:last_error_category"`
	LastSafeError               string     `gorm:"column:last_safe_error"`
	StartedAt                   *time.Time `gorm:"column:started_at"`
	FinishedAt                  *time.Time `gorm:"column:finished_at"`
	ICloudActivationConfirmedAt *time.Time `gorm:"column:icloud_activation_confirmed_at"`
	CreatedAt                   time.Time  `gorm:"column:created_at"`
	UpdatedAt                   time.Time  `gorm:"column:updated_at"`
}

func (iCloudOnboardingTaskModel) TableName() string { return "icloud_account_onboarding_tasks" }

const (
	iCloudAppleIDReservationOnboarding = "onboarding"
	iCloudAppleIDReservationImport     = "cookie_import"
)

type iCloudAppleIDReservationModel struct {
	EmailKey  string    `gorm:"column:email_key;primaryKey"`
	OwnerKind string    `gorm:"column:owner_kind"`
	OwnerID   uint      `gorm:"column:owner_id"`
	CreatedAt time.Time `gorm:"column:created_at"`
}

func (iCloudAppleIDReservationModel) TableName() string { return "icloud_apple_id_reservations" }

type iCloudResourceCredentialModel struct {
	ResourceID      uint      `gorm:"column:resource_id;primaryKey"`
	ApplePassword   string    `gorm:"column:apple_password"`
	SecurityAnswers []byte    `gorm:"column:security_answers;type:json"`
	Birthday        time.Time `gorm:"column:birthday;type:date"`
	CreatedAt       time.Time `gorm:"column:created_at"`
	UpdatedAt       time.Time `gorm:"column:updated_at"`
}

func (iCloudResourceCredentialModel) TableName() string { return "icloud_resource_credentials" }

type iCloudSecurityAnswer struct {
	Question string `json:"question"`
	Answer   string `json:"answer"`
}

type iCloudOnboardingSecret struct {
	Password        string                  `json:"password"`
	SecurityAnswers [3]iCloudSecurityAnswer `json:"securityAnswers"`
	Birthday        string                  `json:"birthday"`
}

type iCloudOnboardingLine struct {
	LineNumber      int
	Region          string
	CountryCode     string
	ICloudOpened    bool
	PrimaryEmail    string
	PhoneNumber     string
	FamilyInviteURL string
	AccountRole     string
	Secret          iCloudOnboardingSecret
}

type OnboardingTaskView struct {
	ID                      uint       `json:"id"`
	TaskKind                string     `json:"taskKind"`
	ResourceID              *uint      `json:"resourceId"`
	LineNumber              int        `json:"lineNumber"`
	PrimaryEmail            string     `json:"primaryEmail"`
	AccountRole             string     `json:"accountRole"`
	FamilyPrimaryResourceID *uint      `json:"familyPrimaryResourceId"`
	FamilyPrimaryEmail      string     `json:"familyPrimaryEmail,omitempty"`
	Region                  string     `json:"region"`
	CountryCode             string     `json:"countryCode"`
	ICloudOpened            bool       `json:"icloudOpened"`
	FamilyInviteURL         string     `json:"familyInviteUrl,omitempty"`
	BoundPhoneNumber        string     `json:"boundPhoneNumber,omitempty"`
	BoundPhoneCountryCode   string     `json:"boundPhoneCountryCode,omitempty"`
	BoundPhoneSource        string     `json:"boundPhoneSource,omitempty"`
	KitesimPhoneID          *uint      `json:"kitesimPhoneId"`
	Status                  string     `json:"status"`
	Stage                   string     `json:"stage"`
	Attempts                int        `json:"attempts"`
	MaxAttempts             int        `json:"maxAttempts"`
	NextAttemptAt           *time.Time `json:"nextAttemptAt"`
	PendingSMSPurpose       string     `json:"pendingSmsPurpose,omitempty"`
	NeedsManualCode         bool       `json:"needsManualCode"`
	NeedsICloudActivation   bool       `json:"needsICloudActivation"`
	NeedsFamilyReset        bool       `json:"needsFamilyReset"`
	NeedsPostFamilyRecovery bool       `json:"needsPostFamilyRecovery"`
	LastErrorCategory       string     `json:"lastErrorCategory,omitempty"`
	LastSafeError           string     `json:"lastSafeError,omitempty"`
	StartedAt               *time.Time `json:"startedAt"`
	FinishedAt              *time.Time `json:"finishedAt"`
	CreatedAt               time.Time  `json:"createdAt"`
	UpdatedAt               time.Time  `json:"updatedAt"`
}

type OnboardingImportView struct {
	ImportID      uint                 `json:"importId"`
	RequestID     string               `json:"requestId"`
	Status        string               `json:"status"`
	Accepted      int                  `json:"accepted"`
	Completed     int                  `json:"completed"`
	Failed        int                  `json:"failed"`
	Waiting       int                  `json:"waiting"`
	LastSafeError string               `json:"lastSafeError,omitempty"`
	Tasks         []OnboardingTaskView `json:"tasks"`
	CreatedAt     time.Time            `json:"createdAt"`
	UpdatedAt     time.Time            `json:"updatedAt"`
}

func parseICloudOnboardingImport(content []byte) ([]iCloudOnboardingLine, error) {
	if !utf8.Valid(content) || strings.TrimSpace(string(content)) == "" {
		return nil, ErrICloudOnboardingInvalid
	}
	lines := make([]iCloudOnboardingLine, 0)
	seen := make(map[string]struct{})
	for index, raw := range strings.Split(string(content), "\n") {
		raw = strings.TrimSuffix(raw, "\r")
		if strings.TrimSpace(raw) == "" {
			continue
		}
		line, err := parseICloudOnboardingLine(index+1, raw)
		if err != nil {
			return nil, err
		}
		if _, exists := seen[line.PrimaryEmail]; exists {
			return nil, fmt.Errorf("%w: duplicate email on line %d", ErrICloudOnboardingInvalid, line.LineNumber)
		}
		seen[line.PrimaryEmail] = struct{}{}
		lines = append(lines, line)
		if len(lines) > iCloudOnboardingMaxLines {
			return nil, ErrICloudOnboardingInvalid
		}
	}
	if len(lines) == 0 {
		return nil, ErrICloudOnboardingInvalid
	}
	return lines, nil
}

func parseICloudOnboardingLine(lineNumber int, raw string) (iCloudOnboardingLine, error) {
	parts := strings.Split(raw, "----")
	if len(parts) < 8 || len(parts) > 10 {
		return iCloudOnboardingLine{}, fmt.Errorf("%w: line %d must contain 8 to 10 fields", ErrICloudOnboardingInvalid, lineNumber)
	}
	for index := range parts {
		parts[index] = strings.TrimSpace(parts[index])
	}
	region := parts[0]
	if region == "" || utf8.RuneCountInString(region) > 64 || strings.ContainsAny(region, "\r\n") {
		return iCloudOnboardingLine{}, fmt.Errorf("%w: invalid region on line %d", ErrICloudOnboardingInvalid, lineNumber)
	}
	opened, ok := parseICloudOpened(parts[1])
	if !ok {
		return iCloudOnboardingLine{}, fmt.Errorf("%w: invalid iCloud flag on line %d", ErrICloudOnboardingInvalid, lineNumber)
	}
	emailValue := strings.ToLower(parts[2])
	address, err := mail.ParseAddress(emailValue)
	if err != nil || address.Address != emailValue || utf8.RuneCountInString(emailValue) > 320 {
		return iCloudOnboardingLine{}, fmt.Errorf("%w: invalid email on line %d", ErrICloudOnboardingInvalid, lineNumber)
	}
	password := parts[3]
	if password == "" || len(password) > 512 || strings.ContainsAny(password, "\r\n") {
		return iCloudOnboardingLine{}, fmt.Errorf("%w: invalid password on line %d", ErrICloudOnboardingInvalid, lineNumber)
	}
	var answers [3]iCloudSecurityAnswer
	for index := range answers {
		answers[index], ok = parseICloudSecurityAnswer(parts[index+4])
		if !ok {
			return iCloudOnboardingLine{}, fmt.Errorf("%w: invalid security answer on line %d", ErrICloudOnboardingInvalid, lineNumber)
		}
	}
	birthday, err := time.Parse("2006-01-02", parts[7])
	if err != nil || birthday.After(time.Now().UTC()) {
		return iCloudOnboardingLine{}, fmt.Errorf("%w: invalid birthday on line %d", ErrICloudOnboardingInvalid, lineNumber)
	}
	phone, invite := "", ""
	if len(parts) == 9 {
		candidate := parts[8]
		phoneShaped := candidate != "" && strings.IndexFunc(candidate, func(char rune) bool {
			return (char < '0' || char > '9') && !strings.ContainsRune("+ -().", char)
		}) == -1
		if candidate != "" {
			if !phoneShaped {
				invite = candidate
			} else {
				phone = onboardingPhoneDigits(candidate)
				if len(phone) < 7 || len(phone) > 20 {
					return iCloudOnboardingLine{}, fmt.Errorf("%w: invalid phone number on line %d", ErrICloudOnboardingInvalid, lineNumber)
				}
			}
		}
	}
	if len(parts) == 10 {
		if candidate := parts[8]; candidate != "" {
			phoneShaped := strings.IndexFunc(candidate, func(char rune) bool {
				return (char < '0' || char > '9') && !strings.ContainsRune("+ -().", char)
			}) == -1
			phone = onboardingPhoneDigits(candidate)
			if !phoneShaped || len(phone) < 7 || len(phone) > 20 {
				return iCloudOnboardingLine{}, fmt.Errorf("%w: invalid phone number on line %d", ErrICloudOnboardingInvalid, lineNumber)
			}
		}
		invite = parts[9]
	}
	if invite != "" || len(parts) == 10 {
		if !validICloudFamilyInvite(invite) {
			return iCloudOnboardingLine{}, fmt.Errorf("%w: invalid family invitation on line %d", ErrICloudOnboardingInvalid, lineNumber)
		}
	}
	role := "child"
	if invite != "" {
		role = "primary"
		if !opened {
			return iCloudOnboardingLine{}, fmt.Errorf("%w: primary account must have iCloud opened on line %d", ErrICloudOnboardingInvalid, lineNumber)
		}
	}
	return iCloudOnboardingLine{
		LineNumber: lineNumber, Region: region, CountryCode: countryCodeFromICloudRegion(region),
		ICloudOpened: opened, PrimaryEmail: emailValue, PhoneNumber: phone,
		FamilyInviteURL: invite, AccountRole: role,
		Secret: iCloudOnboardingSecret{Password: password, SecurityAnswers: answers, Birthday: birthday.Format("2006-01-02")},
	}, nil
}

func parseICloudOpened(value string) (bool, bool) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "是", "yes", "true", "1", "已开通":
		return true, true
	case "否", "no", "false", "0", "未开通":
		return false, true
	default:
		return false, false
	}
}

func parseICloudSecurityAnswer(value string) (iCloudSecurityAnswer, bool) {
	value = strings.TrimSpace(value)
	if !strings.HasSuffix(value, ")") {
		return iCloudSecurityAnswer{}, false
	}
	index := strings.LastIndex(value, "(")
	if index <= 0 {
		return iCloudSecurityAnswer{}, false
	}
	question := strings.TrimSpace(value[:index])
	answer := strings.TrimSpace(value[index+1 : len(value)-1])
	if question == "" || answer == "" || utf8.RuneCountInString(question) > 500 || utf8.RuneCountInString(answer) > 500 || strings.ContainsAny(question+answer, "\r\n") {
		return iCloudSecurityAnswer{}, false
	}
	return iCloudSecurityAnswer{Question: question, Answer: answer}, true
}

func validICloudFamilyInvite(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 2048 || strings.ContainsAny(value, "\r\n") {
		return false
	}
	if !strings.Contains(value, "://") {
		return len(value) <= 512
	}
	parsed, err := url.ParseRequestURI(value)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
		return false
	}
	query := parsed.Query()
	return strings.TrimSpace(query.Get("inviteCode")) != "" || strings.TrimSpace(query.Get("token")) != ""
}

func onboardingPhoneDigits(value string) string {
	var result strings.Builder
	for _, char := range value {
		if char >= '0' && char <= '9' {
			result.WriteRune(char)
		}
	}
	return result.String()
}

func countryCodeFromICloudRegion(region string) string {
	value := strings.ToLower(strings.TrimSpace(strings.TrimSuffix(region, "区")))
	for label, code := range map[string]string{
		"美国": "US", "usa": "US", "us": "US", "加拿大": "CA", "canada": "CA", "ca": "CA",
		"中国": "CN", "大陆": "CN", "china": "CN", "香港": "HK", "台湾": "TW", "澳门": "MO",
		"日本": "JP", "韩国": "KR", "英国": "GB", "澳大利亚": "AU", "澳洲": "AU", "新西兰": "NZ",
		"新加坡": "SG", "马来西亚": "MY", "泰国": "TH", "越南": "VN", "菲律宾": "PH", "印度尼西亚": "ID", "印度": "IN",
		"德国": "DE", "法国": "FR", "意大利": "IT", "西班牙": "ES", "葡萄牙": "PT", "荷兰": "NL", "比利时": "BE",
		"奥地利": "AT", "瑞士": "CH", "瑞典": "SE", "挪威": "NO", "丹麦": "DK", "芬兰": "FI", "波兰": "PL",
		"爱尔兰": "IE", "土耳其": "TR", "墨西哥": "MX", "巴西": "BR", "阿根廷": "AR", "沙特": "SA", "阿联酋": "AE",
	} {
		if value == label {
			return code
		}
	}
	if len(value) == 2 {
		return strings.ToUpper(value)
	}
	return ""
}

func (s *Service) AcceptAdminICloudOnboardingImport(
	ctx context.Context,
	operatorUserID, ownerUserID uint,
	content []byte,
	resourceExpireAt time.Time,
	idempotencyKey, requestID, pathValue string,
) (*OnboardingImportView, bool, error) {
	if err := s.validateICloudImportOwner(ctx, ownerUserID); err != nil {
		return nil, false, err
	}
	if operatorUserID == 0 || strings.TrimSpace(idempotencyKey) == "" || len(idempotencyKey) > 128 || !validICloudResourceExpireAt(resourceExpireAt, s.now().UTC()) {
		return nil, false, ErrICloudOnboardingInvalid
	}
	lines, err := parseICloudOnboardingImport(content)
	if err != nil {
		return nil, false, err
	}
	fingerprint := iCloudOnboardingFingerprint(ownerUserID, resourceExpireAt, content)
	emails := make([]string, len(lines))
	for index, line := range lines {
		emails[index] = iCloudImportEmailKey(line.PrimaryEmail)
	}
	var stored iCloudOnboardingImportModel
	created := false
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("operator_user_id = ? AND idempotency_key = ?", operatorUserID, strings.TrimSpace(idempotencyKey)).
			First(&stored).Error
		if err == nil {
			if stored.RequestFingerprint != fingerprint {
				return ErrICloudOnboardingConflict
			}
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrICloudOnboardingTemporary
		}
		now := s.now().UTC().Truncate(time.Millisecond)
		stored = iCloudOnboardingImportModel{
			OwnerUserID: ownerUserID, OperatorUserID: operatorUserID,
			Status: iCloudOnboardingProcessing, AcceptedCount: len(lines), ResourceExpireAt: normalizeICloudResourceExpireAt(resourceExpireAt),
			RequestID: strings.TrimSpace(requestID), Path: strings.TrimSpace(pathValue),
			IdempotencyKey: strings.TrimSpace(idempotencyKey), RequestFingerprint: fingerprint,
			CreatedAt: now, UpdatedAt: now,
		}
		if err := tx.Create(&stored).Error; err != nil {
			return err
		}
		reservations := make([]iCloudAppleIDReservationModel, len(lines))
		for index, line := range lines {
			reservations[index] = iCloudAppleIDReservationModel{
				EmailKey: iCloudImportEmailKey(line.PrimaryEmail), OwnerKind: iCloudAppleIDReservationOnboarding,
				OwnerID: stored.ID, CreatedAt: now,
			}
		}
		if err := reserveICloudAppleIDsTx(tx, reservations); err != nil {
			if errors.Is(err, ErrICloudResourceIdentity) || isICloudDuplicateError(err) {
				return ErrICloudResourceIdentity
			}
			return ErrICloudOnboardingTemporary
		}
		var existing []struct {
			ID           uint   `gorm:"column:id"`
			OwnerUserID  uint   `gorm:"column:owner_user_id"`
			PrimaryEmail string `gorm:"column:primary_email"`
			AccountRole  string `gorm:"column:account_role"`
			Status       string `gorm:"column:status"`
		}
		if err := tx.Table("icloud_resources AS ir").Clauses(clause.Locking{Strength: "UPDATE"}).
			Select("ir.id, er.owner_user_id, ir.primary_email, ir.account_role, ir.status").
			Joins("JOIN email_resources AS er ON er.id = ir.id AND er.type = ?", "icloud").
			Where("LOWER(ir.primary_email) IN ?", emails).Find(&existing).Error; err != nil {
			return ErrICloudOnboardingTemporary
		}
		existingByEmail := make(map[string]uint, len(existing))
		for _, resource := range existing {
			if resource.OwnerUserID != ownerUserID || resource.AccountRole != "unknown" || resource.Status == iCloudResourceDeleted {
				return ErrICloudResourceIdentity
			}
			existingByEmail[iCloudImportEmailKey(resource.PrimaryEmail)] = resource.ID
		}
		tasks := make([]iCloudOnboardingTaskModel, 0, len(lines))
		for _, line := range lines {
			secret, marshalErr := json.Marshal(line.Secret)
			if marshalErr != nil {
				return ErrICloudOnboardingTemporary
			}
			importID := stored.ID
			var resourceID *uint
			if existingID := existingByEmail[iCloudImportEmailKey(line.PrimaryEmail)]; existingID != 0 {
				value := existingID
				resourceID = &value
			}
			tasks = append(tasks, iCloudOnboardingTaskModel{
				ImportID: &importID, TaskKind: "onboarding", LineNumber: line.LineNumber, PrimaryEmail: line.PrimaryEmail,
				ResourceID:  resourceID,
				AccountRole: line.AccountRole, Region: line.Region, CountryCode: line.CountryCode,
				ICloudOpened: line.ICloudOpened, FamilyInviteURL: line.FamilyInviteURL,
				BoundPhoneNumber: line.PhoneNumber, SecretPayload: secret,
				Status: iCloudOnboardingProcessing, Stage: "accepted", DispatchStatus: "pending",
				Generation: 1, MaxAttempts: iCloudOnboardingMaxAttempts, CreatedAt: now, UpdatedAt: now,
			})
		}
		if err := tx.Create(&tasks).Error; err != nil {
			if isICloudDuplicateError(err) {
				return ErrICloudResourceIdentity
			}
			return err
		}
		if s.operationLogs == nil {
			return ErrICloudImportDependency
		}
		if err := s.operationLogs.CreateInTx(ctx, tx, &governancedomain.OperationLog{
			OperatorUserID: operatorUserID, OperationType: "icloud.admin_account_onboarding.import",
			ResourceType: "icloud_account_onboarding_import", ResourceID: fmt.Sprintf("icloud-onboarding:%d", stored.ID),
			Path: pathValue, Result: "success", SafeSummary: fmt.Sprintf("Accepted %d Apple account onboarding tasks.", len(lines)), RequestID: requestID,
		}); err != nil {
			return ErrICloudOnboardingTemporary
		}
		created = true
		return nil
	})
	if err != nil {
		if errors.Is(err, ErrICloudOnboardingConflict) {
			return nil, false, err
		}
		var existing iCloudOnboardingImportModel
		findErr := s.db.WithContext(ctx).Where("operator_user_id = ? AND idempotency_key = ?", operatorUserID, strings.TrimSpace(idempotencyKey)).First(&existing).Error
		if findErr == nil {
			if existing.RequestFingerprint != fingerprint {
				return nil, false, ErrICloudOnboardingConflict
			}
			stored = existing
			created = false
		} else if !errors.Is(findErr, gorm.ErrRecordNotFound) {
			return nil, false, ErrICloudOnboardingTemporary
		} else if errors.Is(err, ErrICloudResourceIdentity) {
			return nil, false, err
		} else {
			return nil, false, ErrICloudOnboardingTemporary
		}
	}
	view, err := s.GetAdminICloudOnboardingImport(ctx, stored.ID)
	if err != nil {
		return nil, false, err
	}
	if created {
		_ = s.ScheduleICloudOnboardingDispatcher(context.WithoutCancel(ctx), 0)
	}
	return view, !created, nil
}

func (s *Service) GetAdminICloudOnboardingImport(ctx context.Context, importID uint) (*OnboardingImportView, error) {
	if s == nil || s.db == nil || importID == 0 {
		return nil, ErrICloudOnboardingNotFound
	}
	if err := s.refreshICloudOnboardingImport(ctx, importID); err != nil {
		return nil, err
	}
	var model iCloudOnboardingImportModel
	if err := s.db.WithContext(ctx).Preload("Tasks", func(tx *gorm.DB) *gorm.DB { return tx.Order("line_number ASC") }).First(&model, importID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrICloudOnboardingNotFound
		}
		return nil, ErrICloudOnboardingTemporary
	}
	view := &OnboardingImportView{
		ImportID: model.ID, RequestID: model.RequestID, Status: model.Status,
		Accepted: model.AcceptedCount, Completed: model.CompletedCount, Failed: model.FailedCount, Waiting: model.WaitingCount,
		LastSafeError: model.LastSafeError, CreatedAt: model.CreatedAt, UpdatedAt: model.UpdatedAt,
		Tasks: make([]OnboardingTaskView, len(model.Tasks)),
	}
	for index, task := range model.Tasks {
		view.Tasks[index] = iCloudOnboardingTaskView(task)
	}
	if err := s.populateICloudOnboardingFamilyEmails(ctx, view.Tasks); err != nil {
		return nil, err
	}
	return view, nil
}

func (s *Service) GetAdminICloudOnboardingTask(ctx context.Context, taskID uint) (*OnboardingTaskView, error) {
	if s == nil || s.db == nil || taskID == 0 {
		return nil, ErrICloudOnboardingNotFound
	}
	var task iCloudOnboardingTaskModel
	if err := s.db.WithContext(ctx).First(&task, taskID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrICloudOnboardingNotFound
		}
		return nil, ErrICloudOnboardingTemporary
	}
	view := iCloudOnboardingTaskView(task)
	views := []OnboardingTaskView{view}
	if err := s.populateICloudOnboardingFamilyEmails(ctx, views); err != nil {
		return nil, err
	}
	view = views[0]
	return &view, nil
}

func (s *Service) populateICloudOnboardingFamilyEmails(ctx context.Context, tasks []OnboardingTaskView) error {
	ids := make([]uint, 0)
	seen := make(map[uint]struct{})
	for _, task := range tasks {
		if task.FamilyPrimaryResourceID == nil {
			continue
		}
		if _, exists := seen[*task.FamilyPrimaryResourceID]; !exists {
			seen[*task.FamilyPrimaryResourceID] = struct{}{}
			ids = append(ids, *task.FamilyPrimaryResourceID)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	var rows []struct {
		ID           uint   `gorm:"column:id"`
		PrimaryEmail string `gorm:"column:primary_email"`
	}
	if err := s.db.WithContext(ctx).Model(&iCloudResourceModel{}).Select("id, primary_email").Where("id IN ?", ids).Scan(&rows).Error; err != nil {
		return ErrICloudOnboardingTemporary
	}
	byID := make(map[uint]string, len(rows))
	for _, row := range rows {
		byID[row.ID] = row.PrimaryEmail
	}
	for index := range tasks {
		if tasks[index].FamilyPrimaryResourceID != nil {
			tasks[index].FamilyPrimaryEmail = byID[*tasks[index].FamilyPrimaryResourceID]
		}
	}
	return nil
}

func iCloudOnboardingTaskView(task iCloudOnboardingTaskModel) OnboardingTaskView {
	needsPostFamilyRecovery := isICloudPostFamilyRecoveryWaiting(task)
	return OnboardingTaskView{
		ID: task.ID, TaskKind: firstNonEmpty(task.TaskKind, "onboarding"), ResourceID: task.ResourceID, LineNumber: task.LineNumber,
		PrimaryEmail: task.PrimaryEmail, AccountRole: task.AccountRole, FamilyPrimaryResourceID: task.FamilyPrimaryResourceID,
		Region: task.Region, CountryCode: task.CountryCode, ICloudOpened: task.ICloudOpened,
		FamilyInviteURL: task.FamilyInviteURL, BoundPhoneNumber: task.BoundPhoneNumber,
		BoundPhoneCountryCode: task.BoundPhoneCountryCode, BoundPhoneSource: task.BoundPhoneSource,
		KitesimPhoneID: task.KitesimPhoneID, Status: task.Status, Stage: task.Stage,
		Attempts: task.Attempts, MaxAttempts: task.MaxAttempts, NextAttemptAt: task.NextAttemptAt,
		PendingSMSPurpose:       task.PendingSMSPurpose,
		NeedsManualCode:         !needsPostFamilyRecovery && task.Status == iCloudOnboardingWaiting && task.DispatchStatus == "waiting" && task.KitesimPhoneID == nil && task.PendingSMSPurpose != "",
		NeedsICloudActivation:   task.Status == iCloudOnboardingWaiting && task.Stage == "waiting_icloud_activation",
		NeedsFamilyReset:        task.Status == iCloudOnboardingWaiting && task.Stage == "waiting_family_reset",
		NeedsPostFamilyRecovery: needsPostFamilyRecovery,
		LastErrorCategory:       task.LastErrorCategory, LastSafeError: task.LastSafeError,
		StartedAt: task.StartedAt, FinishedAt: task.FinishedAt, CreatedAt: task.CreatedAt, UpdatedAt: task.UpdatedAt,
	}
}

func iCloudOnboardingFingerprint(ownerUserID uint, expireAt time.Time, content []byte) string {
	contentSum := sha256.Sum256(content)
	payload := fmt.Sprintf("icloud-onboarding\x00%d\x00%s\x00%s", ownerUserID, expireAt.UTC().Format(time.RFC3339Nano), hex.EncodeToString(contentSum[:]))
	sum := sha256.Sum256([]byte(payload))
	return hex.EncodeToString(sum[:])
}

func reserveICloudAppleIDsTx(tx *gorm.DB, reservations []iCloudAppleIDReservationModel) error {
	if tx == nil || len(reservations) == 0 {
		return nil
	}
	keys := make([]string, 0, len(reservations))
	wanted := make(map[string]iCloudAppleIDReservationModel, len(reservations))
	for index := range reservations {
		reservations[index].EmailKey = iCloudImportEmailKey(reservations[index].EmailKey)
		if reservations[index].EmailKey == "" || reservations[index].OwnerKind == "" || reservations[index].OwnerID == 0 {
			return ErrICloudResourceIdentity
		}
		keys = append(keys, reservations[index].EmailKey)
		wanted[reservations[index].EmailKey] = reservations[index]
	}
	if len(wanted) != len(reservations) {
		return ErrICloudResourceIdentity
	}
	if err := tx.Clauses(clause.OnConflict{DoNothing: true}).CreateInBatches(&reservations, iCloudImportBatchSize).Error; err != nil {
		return err
	}
	var stored []iCloudAppleIDReservationModel
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("email_key IN ?", keys).Find(&stored).Error; err != nil {
		return err
	}
	if len(stored) != len(wanted) {
		return ErrICloudResourceIdentity
	}
	for _, reservation := range stored {
		expected, ok := wanted[reservation.EmailKey]
		if !ok || reservation.OwnerKind != expected.OwnerKind || reservation.OwnerID != expected.OwnerID {
			return ErrICloudResourceIdentity
		}
	}
	return nil
}

func releaseICloudAppleIDReservationTx(tx *gorm.DB, ownerKind string, ownerID uint, email string) error {
	if tx == nil || ownerKind == "" || ownerID == 0 || iCloudImportEmailKey(email) == "" {
		return nil
	}
	return tx.Where("email_key = ? AND owner_kind = ? AND owner_id = ?", iCloudImportEmailKey(email), ownerKind, ownerID).
		Delete(&iCloudAppleIDReservationModel{}).Error
}

func requireICloudAppleIDReservationTx(tx *gorm.DB, ownerKind string, ownerID uint, email string) error {
	var reservation iCloudAppleIDReservationModel
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("email_key = ?", iCloudImportEmailKey(email)).First(&reservation).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrICloudResourceIdentity
		}
		return err
	}
	if reservation.OwnerKind != ownerKind || reservation.OwnerID != ownerID {
		return ErrICloudResourceIdentity
	}
	return nil
}

func newICloudOnboardingClaimToken() string { return platform.NewUUIDV7String() }
