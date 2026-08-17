package icloud

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/donnel666/remail/internal/kitesim"
)

const (
	appleOnboardingPrepareICloud          = "prepare_icloud"
	appleOnboardingPrepareICloudCookie    = "prepare_icloud_cookie"
	appleOnboardingSendSMS                = "send_sms"
	appleOnboardingVerifySMS              = "verify_sms"
	appleOnboardingFinishICloud           = "finish_icloud"
	appleOnboardingFinishICloudCookie     = "finish_icloud_cookie"
	appleOnboardingPrepareFamily          = "prepare_family"
	appleOnboardingPrepareFamilyReconcile = "prepare_family_reconcile"
	appleOnboardingJoinFamily             = "join_family"
	appleOnboardingPrepareManage          = "prepare_manage"
	appleOnboardingFetchManage            = "fetch_manage"
	appleOnboardingAddForward             = "add_forward"
	appleOnboardingVerifyForward          = "verify_forward"
	appleOnboardingExport                 = "export"

	appleSMSICloudLogin          = "icloud_login"
	appleSMSICloudCookieLogin    = "icloud_cookie_login"
	appleSMSPhoneEnrollment      = "phone_enrollment"
	appleSMSFamilyLogin          = "family_login"
	appleSMSFamilyReconcileLogin = "family_reconcile_login"
	appleSMSManageLogin          = "manage_login"
)

var ErrICloudOnboardingProvider = errors.New("icloud: Apple onboarding provider unavailable")

type AppleOnboardingRequest struct {
	Operation            string
	Email                string
	Secret               iCloudOnboardingSecret
	Session              json.RawMessage
	SMSPurpose           string
	PhoneNumber          string
	PhoneCountryCode     string
	Code                 string
	FamilyInviteURL      string
	FamilyOrganizerEmail string
	ForwardToEmail       string
	ForwardCode          string
	SkipPhoneEnrollment  bool
	SkipOldChannel       bool
}

type AppleOnboardingResponse struct {
	Session             json.RawMessage
	Next                string
	CountryCode         string
	ICloudOpened        *bool
	OldChannel          *AppleOnboardingChannel
	NewChannel          *AppleOnboardingChannel
	FamilyChannel       *AppleOnboardingChannel
	TrustedPhoneLastTwo string
}

type AppleOnboardingChannel struct {
	Kind                  string     `json:"kind"`
	Host                  string     `json:"host"`
	Cookie                string     `json:"cookie"`
	SetupCookie           string     `json:"setupCookie"`
	Origin                string     `json:"origin"`
	Referer               string     `json:"referer"`
	UserAgent             string     `json:"userAgent"`
	FDClientInfo          string     `json:"fdClientInfo"`
	DSID                  string     `json:"dsid"`
	ClientID              string     `json:"clientId"`
	ClientBuildNumber     string     `json:"clientBuildNumber"`
	ClientMasteringNumber string     `json:"clientMasteringNumber"`
	Scnt                  string     `json:"scnt"`
	SessionID             string     `json:"sessionId"`
	APIKey                string     `json:"apiKey"`
	DataAccessToken       string     `json:"dataAccessToken"`
	ManageExpiresAt       *time.Time `json:"manageExpiresAt"`
}

type AppleOnboardingError struct {
	Category     string
	SafeMessage  string
	Retryable    bool
	SendRejected bool
	CodeRejected bool
	RetryAt      *time.Time
	RestartStage string
}

func (e *AppleOnboardingError) Error() string {
	if e == nil || strings.TrimSpace(e.SafeMessage) == "" {
		return ErrICloudOnboardingProvider.Error()
	}
	return e.SafeMessage
}

func (e *AppleOnboardingError) Unwrap() error { return ErrICloudOnboardingProvider }

type AppleOnboardingProvider interface {
	Execute(context.Context, AppleOnboardingRequest) (AppleOnboardingResponse, error)
}

type SMSPhoneService interface {
	BindICloudSMSPhone(context.Context, string, string) (kitesim.SMSPhoneBinding, error)
	BindICloudSMSPhoneBySuffix(context.Context, string, string) (kitesim.SMSPhoneBinding, error)
	ReserveSMSChallenge(context.Context, uint, string, string, time.Time) (kitesim.SMSReservation, error)
	MarkSMSAttemptSent(context.Context, uint64) error
	ConfirmSMSAttemptSent(context.Context, uint64) error
	MarkSMSAttemptSendFailed(context.Context, uint64) error
	MarkSMSAttemptInfrastructureFailed(context.Context, uint64) error
	GetSMSChallengeByOwner(context.Context, string) (kitesim.SMSChallenge, error)
	ClaimAppleSMSMessage(context.Context, uint64) (*kitesim.MessageItem, error)
	CompleteSMSChallenge(context.Context, uint64) error
	CancelSMSChallenge(context.Context, uint64) error
}

type appleOnboardingClient struct {
	newSession appleOnboardingSessionFactory
	endpoints  appleOnboardingEndpoints
	now        func() time.Time
}

func NewAppleOnboardingClient() AppleOnboardingProvider {
	return &appleOnboardingClient{
		newSession: newAppleOnboardingSession,
		endpoints:  defaultAppleOnboardingEndpoints(),
		now:        time.Now,
	}
}

func (c *appleOnboardingClient) Execute(ctx context.Context, request AppleOnboardingRequest) (AppleOnboardingResponse, error) {
	if c == nil || c.newSession == nil || strings.TrimSpace(request.Operation) == "" {
		return AppleOnboardingResponse{}, ErrICloudOnboardingProvider
	}
	flow, err := loadAppleOnboardingFlow(ctx, c, request.Session)
	if err != nil {
		return AppleOnboardingResponse{}, err
	}
	var response AppleOnboardingResponse
	switch request.Operation {
	case appleOnboardingPrepareICloud:
		response, err = flow.prepareICloud(request)
	case appleOnboardingPrepareICloudCookie:
		response, err = flow.prepareICloud(request)
	case appleOnboardingSendSMS:
		response, err = flow.sendSMS(request)
	case appleOnboardingVerifySMS:
		response, err = flow.verifySMS(request)
	case appleOnboardingFinishICloud:
		response, err = flow.finishICloud(request)
	case appleOnboardingFinishICloudCookie:
		response, err = flow.finishICloud(request)
	case appleOnboardingPrepareFamily:
		response, err = flow.prepareFamily(request)
	case appleOnboardingPrepareFamilyReconcile:
		response, err = flow.prepareManage(request)
	case appleOnboardingJoinFamily:
		response, err = flow.joinFamily(request)
	case appleOnboardingPrepareManage:
		response, err = flow.prepareManage(request)
	case appleOnboardingFetchManage:
		response, err = flow.fetchManage(request)
	case appleOnboardingAddForward:
		response, err = flow.addForward(request)
	case appleOnboardingVerifyForward:
		response, err = flow.verifyForward(request)
	case appleOnboardingExport:
		response, err = flow.exportChannels(request)
	default:
		return AppleOnboardingResponse{}, &AppleOnboardingError{Category: "invalid_operation", SafeMessage: "Apple onboarding operation is invalid."}
	}
	if err != nil {
		return AppleOnboardingResponse{}, err
	}
	response.Session, err = flow.snapshot()
	if err != nil {
		return AppleOnboardingResponse{}, err
	}
	return response, nil
}
