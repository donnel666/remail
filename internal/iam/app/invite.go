package app

import (
	"context"
	"encoding/base32"
	"errors"
	"fmt"
	"strings"

	"github.com/donnel666/remail/internal/iam/domain"
)

const (
	referralInviteMaxUse           = 2147483647
	referralInviteCodePrefix       = "AFF"
	referralInviteRandomCodeLength = 10
)

type InviteUseCase struct {
	repo InviteRepository
}

func NewInviteUseCase(repo InviteRepository) *InviteUseCase {
	return &InviteUseCase{repo: repo}
}

func (uc *InviteUseCase) GetReferralInvite(ctx context.Context, userID uint) (*domain.Invite, error) {
	if userID == 0 {
		return nil, domain.ErrAuthenticationRequired
	}
	invite, err := uc.repo.FindReferralInviteByOwner(ctx, userID)
	if err != nil {
		return nil, err
	}
	if invite == nil {
		return nil, domain.ErrInviteNotFound
	}
	return invite, nil
}

func (uc *InviteUseCase) CurrentReferralInvite(ctx context.Context, userID uint) (*domain.Invite, error) {
	if userID == 0 {
		return nil, domain.ErrAuthenticationRequired
	}
	for attempt := 0; attempt < 5; attempt++ {
		code, err := generateReferralInviteCode()
		if err != nil {
			return nil, err
		}
		invite, err := uc.repo.GetOrCreateReferralInvite(ctx, userID, code, referralInviteMaxUse)
		if errors.Is(err, domain.ErrInviteAlreadyExists) {
			continue
		}
		if err != nil {
			return nil, err
		}
		return invite, nil
	}
	return nil, fmt.Errorf("generate referral invite: %w", domain.ErrInviteAlreadyExists)
}

func generateReferralInviteCode() (string, error) {
	randomBytes, err := newCryptoBytes(7)
	if err != nil {
		return "", err
	}
	encoded := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(randomBytes)
	if len(encoded) < referralInviteRandomCodeLength {
		return "", fmt.Errorf("generate referral invite: encoded entropy too short")
	}
	return referralInviteCodePrefix + encoded[:referralInviteRandomCodeLength], nil
}

const (
	batchInviteRandomCodeLength = 14 // ~70 bits base32 entropy
	batchInvitePrefixMaxLength  = 32
)

// generateInviteCodes returns count unique admin invite codes, each the
// (sanitized) prefix followed by a random suffix. Uniqueness is guaranteed
// within the batch; the random suffix makes a collision with existing rows
// negligible.
// ponytail: relies on entropy, not a DB existence check; add regenerate-on-
// conflict if the invites table ever grows enough for birthday collisions.
func generateInviteCodes(prefix string, count int) ([]string, error) {
	prefix = strings.ToUpper(strings.TrimSpace(prefix))
	if len([]rune(prefix)) > batchInvitePrefixMaxLength {
		prefix = string([]rune(prefix)[:batchInvitePrefixMaxLength])
	}
	seen := make(map[string]struct{}, count)
	codes := make([]string, 0, count)
	for len(codes) < count {
		suffix, err := randomInviteSuffix()
		if err != nil {
			return nil, err
		}
		code := prefix + suffix
		if _, ok := seen[code]; ok {
			continue
		}
		seen[code] = struct{}{}
		codes = append(codes, code)
	}
	return codes, nil
}

func randomInviteSuffix() (string, error) {
	randomBytes, err := newCryptoBytes(10)
	if err != nil {
		return "", err
	}
	encoded := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(randomBytes)
	if len(encoded) < batchInviteRandomCodeLength {
		return "", fmt.Errorf("generate invite code: encoded entropy too short")
	}
	return encoded[:batchInviteRandomCodeLength], nil
}
