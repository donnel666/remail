package kitesim

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/donnel666/remail/internal/systemsettings/runtimeconfig"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func setSMSPoolConfig(t *testing.T, key, value string) {
	t.Helper()
	previous := runtimeconfig.String(key, "")
	runtimeconfig.Set(key, value)
	t.Cleanup(func() { runtimeconfig.Set(key, previous) })
}

func newSMSPoolTestService(t *testing.T) (*Service, *gorm.DB, *time.Time) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&accountModel{}, &phoneModel{}, &phoneBindingModel{}, &phoneUsageEventModel{}, &smsChallengeModel{}); err != nil {
		t.Fatal(err)
	}
	account := accountModel{Account: "owner@example.com", Password: "password"}
	if err := db.Create(&account).Error; err != nil {
		t.Fatal(err)
	}
	phones := []phoneModel{
		{AccountID: account.ID, ProviderOrderID: "phone-1", PhoneCode: "1", PhoneNumber: "14165550001", CountryCode: "US", Status: int(PhoneActive)},
		{AccountID: account.ID, ProviderOrderID: "phone-2", PhoneCode: "1", PhoneNumber: "14165550002", CountryCode: "US", Status: int(PhoneActive)},
	}
	if err := db.Create(&phones).Error; err != nil {
		t.Fatal(err)
	}
	clock := time.Date(2026, time.August, 16, 12, 0, 0, 0, time.UTC)
	service := NewService(db, nil)
	service.now = func() time.Time { return clock }
	return service, db, &clock
}

func TestPhoneDisplayAndMatchingAcceptLocalNumber(t *testing.T) {
	binding := SMSPhoneBinding{PhoneCode: "1", PhoneNumber: "15488768536"}
	if got := formatPhoneNumber(binding.PhoneCode, binding.PhoneNumber); got != "+1 5488768536" {
		t.Fatalf("formatted US number = %q", got)
	}
	for _, requested := range []string{"5488768536", "15488768536"} {
		if !samePhoneDigits(binding, requested) {
			t.Fatalf("requested number %q did not match %+v", requested, binding)
		}
	}
	if got := formatPhoneNumber("86", "13600000000"); got != "+86 13600000000" {
		t.Fatalf("formatted CN number = %q", got)
	}
}

func TestBindICloudSMSPhoneBySuffixIsUniqueAndNeverAutoAllocates(t *testing.T) {
	service, db, _ := newSMSPoolTestService(t)
	ctx := context.Background()
	if _, err := service.BindICloudSMSPhoneBySuffix(ctx, "missing@example.com", ""); !errors.Is(err, ErrPhoneMissing) {
		t.Fatalf("empty suffix allocated a phone: %v", err)
	}
	binding, err := service.BindICloudSMSPhoneBySuffix(ctx, "primary@example.com", "0002")
	if err != nil || binding.PhoneNumber != "14165550002" {
		t.Fatalf("unique suffix binding = %+v err=%v", binding, err)
	}
	again, err := service.BindICloudSMSPhoneBySuffix(ctx, "primary@example.com", "")
	if err != nil || again.PhoneID != binding.PhoneID {
		t.Fatalf("empty suffix did not recover permanent binding: %+v err=%v", again, err)
	}

	var account accountModel
	if err := db.First(&account).Error; err != nil {
		t.Fatal(err)
	}
	third := phoneModel{AccountID: account.ID, ProviderOrderID: "phone-3", PhoneCode: "1", PhoneNumber: "14165559901", CountryCode: "US", Status: int(PhoneActive)}
	if err := db.Create(&third).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := service.BindICloudSMSPhoneBySuffix(ctx, "ambiguous@example.com", "01"); !errors.Is(err, ErrSMSPhoneSuffixAmbiguous) {
		t.Fatalf("ambiguous suffix error = %v", err)
	}
	disabledAt := time.Now().UTC()
	if err := db.Model(&phoneModel{}).Where("id = ?", binding.PhoneID).Update("disabled_at", disabledAt).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := service.BindICloudSMSPhoneBySuffix(ctx, "primary@example.com", ""); !errors.Is(err, ErrSMSPhoneBoundUnavailable) {
		t.Fatalf("disabled permanent binding error = %v", err)
	}
	if _, err := service.ReserveSMSChallenge(ctx, binding.PhoneID, "apple_login", "disabled:1", time.Now().UTC().Add(time.Minute)); !errors.Is(err, ErrSMSPhoneBoundUnavailable) {
		t.Fatalf("disabled bound phone reservation error = %v", err)
	}
}

func TestBindICloudSMSPhoneBalancesAndRemainsPermanent(t *testing.T) {
	service, db, _ := newSMSPoolTestService(t)
	ctx := context.Background()
	first, err := service.BindICloudSMSPhone(ctx, "first@example.com", "")
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.BindICloudSMSPhone(ctx, "second@example.com", "")
	if err != nil {
		t.Fatal(err)
	}
	if first.PhoneID == second.PhoneID {
		t.Fatalf("first two accounts used the same phone: %d", first.PhoneID)
	}
	again, err := service.BindICloudSMSPhone(ctx, "first@example.com", first.PhoneNumber)
	if err != nil || again.PhoneID != first.PhoneID {
		t.Fatalf("permanent binding changed: binding=%+v err=%v", again, err)
	}
	if _, err := service.BindICloudSMSPhone(ctx, "first@example.com", second.PhoneNumber); !errors.Is(err, ErrSMSPhoneBindingConflict) {
		t.Fatalf("binding conflict error = %v", err)
	}
	matched, err := service.BindICloudSMSPhone(ctx, "manual@example.com", "4165550002")
	if err != nil || matched.PhoneID != second.PhoneID || matched.Source != "matched" {
		t.Fatalf("manual match = %+v err=%v", matched, err)
	}
	if _, err := service.BindICloudSMSPhone(ctx, "external@example.com", "+1 415 555 9999"); !errors.Is(err, ErrPhoneMissing) {
		t.Fatalf("unmanaged phone error = %v", err)
	}
	var account accountModel
	if err := db.First(&account).Error; err != nil {
		t.Fatal(err)
	}
	china := phoneModel{AccountID: account.ID, ProviderOrderID: "phone-cn", PhoneCode: "86", PhoneNumber: "13600000000", CountryCode: "CN", Status: int(PhoneActive)}
	if err := db.Create(&china).Error; err != nil {
		t.Fatal(err)
	}
	matched, err = service.BindICloudSMSPhone(ctx, "china@example.com", "+86 136 0000 0000")
	if err != nil || matched.PhoneID != china.ID {
		t.Fatalf("international manual match = %+v err=%v", matched, err)
	}
}

func TestBindICloudSMSPhoneRejectsConflictingRaceWinner(t *testing.T) {
	service, db, _ := newSMSPoolTestService(t)
	var first, second phoneModel
	if err := db.Where("phone_number = ?", "14165550001").First(&first).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Where("phone_number = ?", "14165550002").First(&second).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("CREATE UNIQUE INDEX uk_test_binding_consumer ON kitesim_phone_bindings(consumer_type, consumer_key)").Error; err != nil {
		t.Fatal(err)
	}
	const callback = "test:icloud_binding_race"
	if err := db.Callback().Create().Before("gorm:create").Register(callback, func(tx *gorm.DB) {
		row, ok := tx.Statement.Dest.(*phoneBindingModel)
		if !ok || row.ConsumerKey != "race@example.com" || row.PhoneID != first.ID {
			return
		}
		if err := tx.Session(&gorm.Session{NewDB: true}).Exec(
			"INSERT INTO kitesim_phone_bindings(phone_id, consumer_type, consumer_key, source, created_at) VALUES (?, ?, ?, ?, ?)",
			second.ID, smsConsumerICloud, row.ConsumerKey, "matched", time.Now().UTC(),
		).Error; err != nil {
			_ = tx.AddError(err)
		}
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.BindICloudSMSPhone(context.Background(), "race@example.com", first.PhoneNumber); !errors.Is(err, ErrSMSPhoneBindingConflict) {
		t.Fatalf("race winner mismatch error = %v", err)
	}
}

func TestReserveSMSAttemptAppliesCooldownAndHourlyLimit(t *testing.T) {
	setSMSPoolConfig(t, runtimeconfig.ICloudPhoneHourlySMSLimitKey, "4")
	setSMSPoolConfig(t, runtimeconfig.ICloudPhoneCooldownBaseSecondsKey, "5")
	setSMSPoolConfig(t, runtimeconfig.ICloudPhoneCooldownMaxSecondsKey, "9")
	service, db, clock := newSMSPoolTestService(t)
	ctx := context.Background()
	binding, err := service.BindICloudSMSPhone(ctx, "first@example.com", "")
	if err != nil {
		t.Fatal(err)
	}
	wantDelays := []time.Duration{5 * time.Second, 9 * time.Second, 9 * time.Second}
	for index, wantDelay := range wantDelays {
		reservation, reserveErr := service.ReserveSMSAttempt(ctx, binding.PhoneID, "test")
		if reserveErr != nil {
			t.Fatalf("reservation %d: %v", index+1, reserveErr)
		}
		if got := reservation.CooldownUntil.Sub(*clock); got != wantDelay {
			t.Fatalf("reservation %d cooldown = %v, want %v", index+1, got, wantDelay)
		}
		if _, reserveErr = service.ReserveSMSAttempt(ctx, binding.PhoneID, "test"); !errors.Is(reserveErr, ErrSMSPhoneUnavailable) {
			t.Fatalf("reservation %d ignored cooldown: %v", index+1, reserveErr)
		}
		*clock = reservation.CooldownUntil
		if err := service.MarkSMSAttemptInfrastructureFailed(ctx, reservation.ID); err != nil {
			t.Fatal(err)
		}
	}
	for index := 3; index < 4; index++ {
		reservation, reserveErr := service.ReserveSMSAttempt(ctx, binding.PhoneID, "test")
		if reserveErr != nil {
			t.Fatalf("reservation %d: %v", index+1, reserveErr)
		}
		*clock = reservation.CooldownUntil
		if err := service.MarkSMSAttemptInfrastructureFailed(ctx, reservation.ID); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := service.ReserveSMSAttempt(ctx, binding.PhoneID, "test"); !errors.Is(err, ErrSMSPhoneUnavailable) {
		t.Fatalf("hourly limit error = %v", err)
	} else if retryAt, ok := SMSRetryAt(err); !ok || !retryAt.After(*clock) {
		t.Fatalf("hourly retry = %v ok=%v current=%v", retryAt, ok, *clock)
	}
	var count int64
	if err := db.Model(&phoneUsageEventModel{}).Where("phone_id = ?", binding.PhoneID).Count(&count).Error; err != nil || count != 4 {
		t.Fatalf("usage count = %d err=%v", count, err)
	}
}

func TestSMSAttemptFailuresBlacklistPhoneAndSuccessResetsCounter(t *testing.T) {
	setSMSPoolConfig(t, runtimeconfig.ICloudPhoneSendFailureThresholdKey, "2")
	setSMSPoolConfig(t, runtimeconfig.ICloudPhoneBlacklistHoursKey, "6")
	service, db, clock := newSMSPoolTestService(t)
	ctx := context.Background()
	binding, err := service.BindICloudSMSPhone(ctx, "first@example.com", "")
	if err != nil {
		t.Fatal(err)
	}
	for attempt := 0; attempt < 2; attempt++ {
		reservation, reserveErr := service.ReserveSMSAttempt(ctx, binding.PhoneID, "test")
		if reserveErr != nil {
			t.Fatal(reserveErr)
		}
		if err := service.MarkSMSAttemptSent(ctx, reservation.ID); err != nil {
			t.Fatal(err)
		}
		if err := service.MarkSMSAttemptSendFailed(ctx, reservation.ID); err != nil {
			t.Fatal(err)
		}
		*clock = reservation.CooldownUntil
	}
	var phone phoneModel
	if err := db.First(&phone, binding.PhoneID).Error; err != nil {
		t.Fatal(err)
	}
	if phone.SMSConsecutiveFailures != 2 || phone.SMSBlacklistedUntil == nil || phone.SMSLastUsedAt == nil || !phone.SMSBlacklistedUntil.Equal(phone.SMSLastUsedAt.Add(6*time.Hour)) {
		t.Fatalf("blacklist state = %+v current=%v", phone, *clock)
	}
	if _, err := service.ReserveSMSAttempt(ctx, binding.PhoneID, "test"); !errors.Is(err, ErrSMSPhoneUnavailable) {
		t.Fatalf("blacklisted reservation error = %v", err)
	}
	*clock = *phone.SMSBlacklistedUntil
	reservation, err := service.ReserveSMSAttempt(ctx, binding.PhoneID, "test")
	if err != nil {
		t.Fatal(err)
	}
	if err := service.MarkSMSAttemptSendFailed(ctx, reservation.ID); err != nil {
		t.Fatal(err)
	}
	phone = phoneModel{}
	if err := db.First(&phone, binding.PhoneID).Error; err != nil || phone.SMSConsecutiveFailures != 1 || phone.SMSBlacklistedUntil != nil {
		t.Fatalf("expired blacklist did not start a new failure cycle: %+v err=%v", phone, err)
	}

	*clock = reservation.CooldownUntil
	reservation, err = service.ReserveSMSAttempt(ctx, binding.PhoneID, "test")
	if err != nil {
		t.Fatal(err)
	}
	activeBlacklist := clock.Add(6 * time.Hour)
	if err := db.Model(&phoneModel{}).Where("id = ?", binding.PhoneID).Updates(map[string]any{
		"sms_consecutive_failures": 2,
		"sms_blacklisted_until":    activeBlacklist,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := service.MarkSMSAttemptSent(ctx, reservation.ID); err != nil {
		t.Fatal(err)
	}
	phone = phoneModel{}
	if err := db.First(&phone, binding.PhoneID).Error; err != nil || phone.SMSConsecutiveFailures != 2 || phone.SMSBlacklistedUntil == nil {
		t.Fatalf("may-have-sent write reset failure state early: %+v err=%v", phone, err)
	}
	if err := service.ConfirmSMSAttemptSent(ctx, reservation.ID); err != nil {
		t.Fatal(err)
	}
	phone = phoneModel{}
	if err := db.First(&phone, binding.PhoneID).Error; err != nil || phone.SMSConsecutiveFailures != 0 || phone.SMSBlacklistedUntil != nil {
		t.Fatalf("confirmed send did not reset failure and blacklist state: %+v err=%v", phone, err)
	}
}

func TestSMSChallengeBlocksPhoneAndRecoversByOwner(t *testing.T) {
	setSMSPoolConfig(t, runtimeconfig.ICloudPhoneCooldownBaseSecondsKey, "1")
	setSMSPoolConfig(t, runtimeconfig.ICloudPhoneCooldownMaxSecondsKey, "1")
	service, db, clock := newSMSPoolTestService(t)
	ctx := context.Background()
	binding, err := service.BindICloudSMSPhone(ctx, "first@example.com", "")
	if err != nil {
		t.Fatal(err)
	}
	expiresAt := clock.Add(2 * time.Minute)
	first, err := service.ReserveSMSChallenge(ctx, binding.PhoneID, "apple_manage", "task-1:manage:1", expiresAt)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.MarkSMSAttemptSent(ctx, first.ID); err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&phoneModel{}).Where("id = ?", binding.PhoneID).Update("sms_consecutive_failures", 2).Error; err != nil {
		t.Fatal(err)
	}
	replayed, err := service.ReserveSMSChallenge(ctx, binding.PhoneID, "apple_manage", "task-1:manage:1", expiresAt)
	if err != nil || replayed.ID != first.ID || replayed.Status != SMSChallengeSent {
		t.Fatalf("owner replay = %+v err=%v", replayed, err)
	}
	if _, err := service.ReserveSMSChallenge(ctx, binding.PhoneID, "apple_manage", "task-2:manage:1", expiresAt); !errors.Is(err, ErrSMSPhoneUnavailable) {
		t.Fatalf("second active challenge error = %v", err)
	} else if retryAt, ok := SMSRetryAt(err); !ok || !retryAt.Equal(expiresAt) {
		t.Fatalf("active challenge retry = %v ok=%v", retryAt, ok)
	}
	*clock = clock.Add(time.Second)
	if err := service.CompleteSMSChallenge(ctx, first.ID); err != nil {
		t.Fatal(err)
	}
	var phone phoneModel
	if err := db.First(&phone, binding.PhoneID).Error; err != nil || phone.SMSConsecutiveFailures != 0 {
		t.Fatalf("verified challenge did not reset failure cycle: %+v err=%v", phone, err)
	}
	stored, err := service.GetSMSChallenge(ctx, first.ID)
	if err != nil || stored.Status != SMSChallengeCompleted || stored.FinishedAt == nil {
		t.Fatalf("completed challenge = %+v err=%v", stored, err)
	}
	second, err := service.ReserveSMSChallenge(ctx, binding.PhoneID, "apple_manage", "task-1:manage:1", clock.Add(2*time.Minute))
	if err != nil || second.ID == first.ID {
		t.Fatalf("completed owner did not start a new challenge: %+v err=%v", second, err)
	}
	byOwner, err := service.GetSMSChallengeByOwner(ctx, "task-1:manage:1")
	if err != nil || byOwner.ID != second.ID {
		t.Fatalf("completed owner was not transferred: %+v err=%v", byOwner, err)
	}
	var old smsChallengeModel
	if err := db.First(&old, first.ID).Error; err != nil || old.OwnerKey != nil {
		t.Fatalf("completed challenge retained owner: %+v err=%v", old, err)
	}
	if err := service.CancelSMSChallenge(ctx, second.ID); err != nil {
		t.Fatal(err)
	}
	*clock = second.CooldownUntil
	third, err := service.ReserveSMSChallenge(ctx, binding.PhoneID, "apple_manage", "task-1:manage:1", clock.Add(2*time.Minute))
	if err != nil || third.ID == second.ID {
		t.Fatalf("canceled owner did not start a new challenge: %+v err=%v", third, err)
	}
	old = smsChallengeModel{}
	if err := db.First(&old, second.ID).Error; err != nil || old.OwnerKey != nil {
		t.Fatalf("canceled challenge retained owner: %+v err=%v", old, err)
	}
}

func TestSMSChallengeSendClaimCanOnlyBeTakenOnce(t *testing.T) {
	service, _, clock := newSMSPoolTestService(t)
	binding, err := service.BindICloudSMSPhone(context.Background(), "first@example.com", "")
	if err != nil {
		t.Fatal(err)
	}
	reservation, err := service.ReserveSMSChallenge(
		context.Background(), binding.PhoneID, "apple_login", "task-1:login:1", clock.Add(2*time.Minute),
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.MarkSMSAttemptSent(context.Background(), reservation.ID); err != nil {
		t.Fatal(err)
	}
	if err := service.MarkSMSAttemptSent(context.Background(), reservation.ID); !errors.Is(err, ErrSMSChallengeInactive) {
		t.Fatalf("second sender claimed the same challenge: %v", err)
	}
}

func TestSMSChallengeExpiryReleasesPhone(t *testing.T) {
	setSMSPoolConfig(t, runtimeconfig.ICloudPhoneCooldownBaseSecondsKey, "1")
	setSMSPoolConfig(t, runtimeconfig.ICloudPhoneCooldownMaxSecondsKey, "1")
	service, _, clock := newSMSPoolTestService(t)
	ctx := context.Background()
	binding, err := service.BindICloudSMSPhone(ctx, "first@example.com", "")
	if err != nil {
		t.Fatal(err)
	}
	first, err := service.ReserveSMSChallenge(ctx, binding.PhoneID, "apple_login", "task-1:login:1", clock.Add(30*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if err := service.MarkSMSAttemptSent(ctx, first.ID); err != nil {
		t.Fatal(err)
	}
	*clock = clock.Add(31 * time.Second)
	if _, err := service.ReserveSMSChallenge(ctx, binding.PhoneID, "apple_login", "task-2:login:1", clock.Add(2*time.Minute)); err != nil {
		t.Fatalf("expired challenge did not release phone: %v", err)
	}
	stored, err := service.GetSMSChallenge(ctx, first.ID)
	if err != nil || stored.Status != SMSChallengeExpired {
		t.Fatalf("expired challenge = %+v err=%v", stored, err)
	}
}

func TestClaimAppleSMSMessageFiltersWindowAndConsumesMessage(t *testing.T) {
	setSMSPoolConfig(t, runtimeconfig.ICloudPhoneCooldownBaseSecondsKey, "1")
	setSMSPoolConfig(t, runtimeconfig.ICloudPhoneCooldownMaxSecondsKey, "1")
	service, _, clock := newSMSPoolTestService(t)
	ctx := context.Background()
	binding, err := service.BindICloudSMSPhone(ctx, "first@example.com", "")
	if err != nil {
		t.Fatal(err)
	}
	first, err := service.ReserveSMSChallenge(ctx, binding.PhoneID, "apple_login", "task-1:login:1", clock.Add(2*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if err := service.MarkSMSAttemptSent(ctx, first.ID); err != nil {
		t.Fatal(err)
	}
	messages := []MessageItem{
		{Caller: "Apple", Content: "code 111111", Time: clock.Add(time.Second).Format(time.RFC3339)},
		{Caller: "106", Content: "Your Apple Account Code is: 222222. Don't share it with anyone.", Time: clock.Add(-time.Minute).Format(time.RFC3339)},
		{Caller: "106", Content: "你的Apple账户验证码是 333333，切勿向任何人泄露，以防账户或信息被盗。", Time: "2026-08-16 20:00:05"},
	}
	*clock = clock.Add(10 * time.Second)
	claimed, err := service.claimAppleSMSMessage(ctx, first.ID, messages)
	if err != nil || appleSMSCode(claimed.Content) != "333333" {
		t.Fatalf("first claim = %+v err=%v", claimed, err)
	}
	replayed, err := service.claimAppleSMSMessage(ctx, first.ID, nil)
	if err != nil || replayed.Content != claimed.Content {
		t.Fatalf("persisted claim = %+v err=%v", replayed, err)
	}
	if err := service.CompleteSMSChallenge(ctx, first.ID); err != nil {
		t.Fatal(err)
	}

	second, err := service.ReserveSMSChallenge(ctx, binding.PhoneID, "apple_login", "task-2:login:1", clock.Add(2*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if err := service.MarkSMSAttemptSent(ctx, second.ID); err != nil {
		t.Fatal(err)
	}
	newMessage := MessageItem{Content: "Your Apple Account Code is: 444444. Don't share it with anyone.", Time: clock.Add(2 * time.Second).Format(time.RFC3339)}
	*clock = clock.Add(3 * time.Second)
	claimed, err = service.claimAppleSMSMessage(ctx, second.ID, []MessageItem{messages[2], newMessage})
	if err != nil || appleSMSCode(claimed.Content) != "444444" {
		t.Fatalf("consumed message was reused: %+v err=%v", claimed, err)
	}
}

func TestParseProviderTimeUsesShanghaiForNaiveValues(t *testing.T) {
	want := time.Date(2026, time.August, 16, 12, 0, 5, 0, time.UTC)
	for _, value := range []string{"2026-08-16 20:00:05", "2026-08-16T20:00:05+08:00"} {
		got := parseProviderTime(value)
		if got == nil || !got.Equal(want) {
			t.Fatalf("parseProviderTime(%q) = %v, want %v", value, got, want)
		}
	}
}
