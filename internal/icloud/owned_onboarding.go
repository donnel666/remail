package icloud

import (
	"context"
	"encoding/json"
	"strings"
)

const iCloudUserImportTaskKind = "user_import"

func isICloudOnboardingTaskKind(kind string) bool {
	kind = strings.TrimSpace(kind)
	return kind == "" || kind == "onboarding" || kind == iCloudUserImportTaskKind
}

// User imports can only perform management login, forwarding setup and session export.
func (s *Service) processOwnedICloudImportStage(ctx context.Context, task *iCloudOnboardingTaskModel) error {
	if iCloudOnboardingTaskPhase(task) != 3 {
		return s.failICloudOnboardingTask(ctx, task, "invalid_stage", "User imports only support account management and forwarding setup.")
	}
	if !validPublicDeviceCodeURL(task.DeviceCodeAPI) {
		return s.failICloudOnboardingTask(ctx, task, "invalid_device_code_url", "A public device verification code URL is required.")
	}
	var secret iCloudOnboardingSecret
	if json.Unmarshal(task.SecretPayload, &secret) != nil || strings.TrimSpace(secret.Password) == "" {
		return s.failICloudOnboardingTask(ctx, task, "invalid_credentials", "Stored Apple credentials are invalid.")
	}
	switch task.Stage {
	case "manage_prepare":
		return s.prepareICloudOnboardingApple(ctx, task, secret, appleOnboardingPrepareManage, "manage_profile")
	case "manage_profile":
		return s.fetchICloudOnboardingManage(ctx, task, secret)
	case "sms_send":
		return s.sendICloudOnboardingSMS(ctx, task, secret)
	case "sms_wait":
		return s.waitOnboardingDeviceCode(ctx, task)
	case "sms_verify":
		return s.verifyICloudOnboardingSMS(ctx, task, secret)
	case "sms_verify_recover":
		return s.recoverICloudOnboardingSMSVerification(ctx, task)
	case "forwarding_prepare":
		return s.prepareICloudOnboardingForwarding(ctx, task)
	case "forwarding_add_intent":
		return s.advanceICloudOnboardingTask(ctx, task, "forwarding_add_apply", nil, nil)
	case "forwarding_add_apply":
		return s.sendICloudOnboardingForwarding(ctx, task, secret)
	case "forwarding_wait":
		return s.waitICloudOnboardingForwarding(ctx, task)
	case "forwarding_verify_intent":
		return s.advanceICloudOnboardingTask(ctx, task, "forwarding_verify_apply", nil, nil)
	case "forwarding_verify_apply":
		return s.verifyICloudOnboardingForwarding(ctx, task, secret)
	case "resource_import":
		return s.importICloudOnboardingResource(ctx, task, secret)
	default:
		return s.failICloudOnboardingTask(ctx, task, "invalid_stage", "User import stage is invalid.")
	}
}
