package main

import (
	"errors"
	"strings"
	"time"

	"github.com/donnel666/remail/internal/icloud"
	"github.com/donnel666/remail/internal/kitesim"
)

type privateEmailReplacementCheckpoint struct {
	Target        string `json:"target"`
	ID            string `json:"id,omitempty"`
	Candidate     string `json:"candidate,omitempty"`
	CreateStarted bool   `json:"createStarted,omitempty"`
	Completed     bool   `json:"completed,omitempty"`
}

func (d *debugger) replacePrivateEmail(binding *kitesim.SMSPhoneBinding, config options) error {
	if d.checkpoint == nil || d.state == nil || d.stateKey == "" || d.statePath == "" || d.runtime.icloud == nil {
		return errors.New("private email replacement requires a saved checkpoint and the resource service")
	}
	replacement := d.checkpoint.PrivateEmailReplacement
	if replacement == nil {
		replacement = &privateEmailReplacementCheckpoint{Target: config.replacePrivateEmail}
		if err := d.markCheckpoint(func(cp *accountCheckpoint) { cp.PrivateEmailReplacement = replacement }); err != nil {
			return err
		}
	} else if replacement.Target != config.replacePrivateEmail {
		return errors.New("checkpoint belongs to another private email replacement; use a separate -state file")
	}
	if replacement.Completed {
		d.logf("private_email=already_replaced old=%s new=%s\n", replacement.Target, replacement.Candidate)
		return nil
	}
	if err := d.runtime.icloud.PrepareStandalonePrivateEmailReplacement(d.ctx, config.ownerUserID, d.input.Email, config.replacePrivateEmail); err != nil {
		return err
	}
	if !temporaryManageSessionReady(d.checkpoint, time.Now().UTC()) {
		if d.checkpoint.PendingSMSPurpose != icloud.AppleSMSManageLogin || len(d.session) == 0 {
			if err := d.restartAt("manage_prepare"); err != nil {
				return err
			}
		}
		if err := d.checkSMSPhoneBeforePhase(binding, "manage"); err != nil {
			return err
		}
		if _, err := d.auth(icloud.AppleOnboardingPrepareManage, "private email management login", binding); err != nil {
			return err
		}
		if err := d.markCheckpoint(func(cp *accountCheckpoint) {
			cp.ManageAuthenticated = true
			cp.ManageSessionExpiresAt = time.Now().UTC().Add(temporaryManageSessionTTL)
			cp.Stage = "manage_authenticated"
		}); err != nil {
			return err
		}
	}
	if _, err := d.execute(icloud.AppleOnboardingRequest{Operation: icloud.AppleOnboardingFetchManage, SkipPrivateAlias: true}); err != nil {
		return err
	}
	var channel *icloud.AppleOnboardingChannel
	list := func() ([]icloud.ApplePrivateEmail, error) {
		response, err := d.execute(icloud.AppleOnboardingRequest{Operation: icloud.AppleOnboardingListPrivateEmails})
		channel = response.NewChannel
		return response.PrivateEmails, err
	}
	aliases, err := list()
	if err != nil {
		return err
	}
	target := findPrivateEmail(aliases, replacement.Target)
	if replacement.ID == "" {
		if target == nil {
			return errors.New("the specified private email does not belong to this Apple account")
		}
		if err := d.markCheckpoint(func(*accountCheckpoint) { replacement.ID = target.ID }); err != nil {
			return err
		}
	}
	if target != nil && target.ID != replacement.ID {
		return errors.New("private email identity changed; refusing to delete another address")
	}
	for _, operation := range []string{icloud.AppleOnboardingStopPrivateEmail, icloud.AppleOnboardingRemovePrivateEmail} {
		if target == nil {
			break
		}
		if operation == icloud.AppleOnboardingStopPrivateEmail && target.Active != nil && !*target.Active {
			continue
		}
		if _, err := d.execute(icloud.AppleOnboardingRequest{Operation: operation, PrivateEmailID: replacement.ID, PrivateEmailAddress: replacement.Target}); err != nil {
			return err
		}
		aliases, err = list()
		if err != nil {
			return err
		}
		target = findPrivateEmail(aliases, replacement.Target)
		if target != nil && (target.ID != replacement.ID || target.Active == nil || *target.Active || operation == icloud.AppleOnboardingRemovePrivateEmail) {
			return errors.New("private email change was not confirmed by Apple; rerun to check its state")
		}
		d.logf("private_email=%s target=%s confirmed=true\n", operation, replacement.Target)
	}
	if err := d.runtime.icloud.SyncStandalonePrivateEmails(d.ctx, config.ownerUserID, d.input.Email, replacement.Target, aliases, channel); err != nil {
		return err
	}
	if replacement.Candidate == "" {
		generated, err := d.execute(icloud.AppleOnboardingRequest{Operation: icloud.AppleOnboardingGeneratePrivateEmail})
		if err != nil {
			return err
		}
		if generated.PrivateEmailAddress == "" || strings.EqualFold(generated.PrivateEmailAddress, replacement.Target) || findPrivateEmail(aliases, generated.PrivateEmailAddress) != nil {
			return errors.New("replacement candidate from Apple is invalid or already exists")
		}
		if err := d.markCheckpoint(func(*accountCheckpoint) { replacement.Candidate = generated.PrivateEmailAddress }); err != nil {
			return err
		}
	}
	created := findPrivateEmail(aliases, replacement.Candidate)
	if created == nil {
		if replacement.CreateStarted {
			return errors.New("replacement creation is unconfirmed; rerun to inspect the same candidate without sending another create request")
		}
		if err := d.markCheckpoint(func(*accountCheckpoint) { replacement.CreateStarted = true }); err != nil {
			return err
		}
		d.logf("private_email=creating candidate=%s\n", replacement.Candidate)
		if _, err := d.execute(icloud.AppleOnboardingRequest{Operation: icloud.AppleOnboardingCompletePrivateEmail, PrivateEmailAddress: replacement.Candidate}); err != nil {
			var rejection *icloud.AppleOnboardingError
			if errors.As(err, &rejection) && rejection.HTTPStatus >= 400 && rejection.HTTPStatus < 500 && rejection.HTTPStatus != 408 {
				if saveErr := d.markCheckpoint(func(*accountCheckpoint) { replacement.CreateStarted = false }); saveErr != nil {
					return errors.Join(err, saveErr)
				}
			}
			return err
		}
		aliases, err = list()
		if err != nil {
			return err
		}
		created = findPrivateEmail(aliases, replacement.Candidate)
	}
	if created == nil || created.Active == nil || !*created.Active || findPrivateEmail(aliases, replacement.Target) != nil {
		return errors.New("replacement is incomplete: Apple has not confirmed the new active private email and removal of the old address")
	}
	if err := d.runtime.icloud.SyncStandalonePrivateEmails(d.ctx, config.ownerUserID, d.input.Email, replacement.Target, aliases, channel); err != nil {
		return err
	}
	if err := d.markCheckpoint(func(*accountCheckpoint) { replacement.Completed = true }); err != nil {
		return err
	}
	d.logf("private_email=replaced old=%s new=%s\n", replacement.Target, replacement.Candidate)
	return nil
}

func findPrivateEmail(aliases []icloud.ApplePrivateEmail, email string) *icloud.ApplePrivateEmail {
	for i := range aliases {
		if strings.EqualFold(aliases[i].EmailAddress, email) {
			return &aliases[i]
		}
	}
	return nil
}
