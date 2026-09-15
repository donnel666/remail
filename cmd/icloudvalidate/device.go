package main

import (
	"errors"
	"time"

	"github.com/donnel666/remail/internal/icloud"
	"github.com/donnel666/remail/internal/kitesim"
)

func (d *debugger) ensureDeviceBinding(phone *kitesim.SMSPhoneBinding) error {
	if d.checkpoint == nil {
		return errors.New("device binding requires a durable checkpoint")
	}
	if d.checkpoint.DeviceCodeAPI != "" {
		return nil
	}
	if !icloud.DeviceCodeConfigured() && d.checkpoint.DeviceBindStatus == "" {
		return nil
	}
	if phone == nil || d.runtime.icloud == nil {
		return errors.New("device binding requires the initially bound phone and resource service")
	}
	if err := d.markCheckpoint(func(cp *accountCheckpoint) {
		cp.DeviceBindStatus = "pending"
		if cp.PhoneConfirmedAt.IsZero() {
			cp.PhoneConfirmedAt = time.Now().UTC()
		}
	}); err != nil {
		return err
	}
	for {
		binding, err := d.runtime.icloud.EnsureDeviceBinding(d.ctx, d.input.Email, d.input.Secret.Password, phone.PhoneID, phone.PhoneNumber, nil, d.checkpoint.PhoneConfirmedAt)
		if err != nil {
			return err
		}
		if binding.Status == "failed" {
			return errors.New(binding.LastError)
		}
		if binding.Status == "success" && binding.CodeAPI != "" {
			return d.markCheckpoint(func(cp *accountCheckpoint) {
				cp.DeviceCodeAPI = binding.CodeAPI
				cp.DeviceAccountID = binding.RemoteID
				cp.DeviceBindStatus = "success"
			})
		}
		d.logf("device_binding=waiting status=%s\n", binding.Status)
		// The shared Asynq task queries upstream. CMD only reads its durable result.
		if err := d.waitUntil(time.Now().Add(10 * time.Second)); err != nil {
			return err
		}
	}
}

func (d *debugger) deviceRound(purpose string) (icloud.AppleOnboardingResponse, error) {
	if d.runtime.icloud == nil {
		return icloud.AppleOnboardingResponse{}, errors.New("device code service unavailable")
	}
	if purpose == icloud.AppleSMSPhoneEnrollment {
		return icloud.AppleOnboardingResponse{}, errors.New("device codes cannot enroll the initial phone")
	}
	deadline := time.Now().Add(2 * time.Minute)
	for time.Now().Before(deadline) {
		code, err := d.runtime.icloud.FetchDeviceCode(d.ctx, d.checkpoint.DeviceCodeAPI)
		if err == nil {
			response, err := d.execute(icloud.AppleOnboardingRequest{Operation: icloud.AppleOnboardingVerifySMS, SMSPurpose: purpose, Code: code, UseDeviceCode: true})
			if err != nil {
				return icloud.AppleOnboardingResponse{}, err
			}
			if err := d.markCheckpoint(func(cp *accountCheckpoint) { cp.PendingSMSPurpose = ""; cp.Stage = "device_verified" }); err != nil {
				return icloud.AppleOnboardingResponse{}, err
			}
			return response, nil
		}
		if err := d.waitUntil(time.Now().Add(4 * time.Second)); err != nil {
			return icloud.AppleOnboardingResponse{}, err
		}
	}
	return icloud.AppleOnboardingResponse{}, errors.New("device code polling timed out; SMS fallback is disabled")
}
