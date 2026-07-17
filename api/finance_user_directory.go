package api

import (
	"context"

	billingapp "github.com/donnel666/remail/internal/billing/app"
	iamdomain "github.com/donnel666/remail/internal/iam/domain"
)

// financeUserSummarySource is the slice of the IAM user repository that finance
// read models need. *iam/infra.UserRepo satisfies it; the seam keeps the
// composition root free of iam/infra internals and makes the mapping testable.
type financeUserSummarySource interface {
	LookupUserSummaries(ctx context.Context, ids []uint) (map[uint]iamdomain.UserSummary, error)
	ListUserSummaries(ctx context.Context, search string, offset, limit int) ([]iamdomain.UserSummary, int, error)
}

// financeUserDirectory adapts the IAM user repository to billing's UserDirectory
// port. It enriches finance read models (cards, transactions, wallets) with user
// identity and drives the admin balances list. It lives in the composition root
// so IAM need not depend on billing, mirroring how AdminUserSelectionResolver is
// wired into billing's UserSelectionResolver.
type financeUserDirectory struct {
	users financeUserSummarySource
}

func (d financeUserDirectory) LookupUsers(ctx context.Context, ids []uint) (map[uint]billingapp.UserDirectoryEntry, error) {
	if d.users == nil || len(ids) == 0 {
		return map[uint]billingapp.UserDirectoryEntry{}, nil
	}
	summaries, err := d.users.LookupUserSummaries(ctx, ids)
	if err != nil {
		return nil, err
	}
	out := make(map[uint]billingapp.UserDirectoryEntry, len(summaries))
	for id, summary := range summaries {
		out[id] = financeDirectoryEntry(summary)
	}
	return out, nil
}

func (d financeUserDirectory) ListUsers(ctx context.Context, q billingapp.UserDirectoryQuery) (billingapp.UserDirectoryPage, error) {
	if d.users == nil {
		return billingapp.UserDirectoryPage{}, nil
	}
	items, total, err := d.users.ListUserSummaries(ctx, q.Search, q.Offset, q.Limit)
	if err != nil {
		return billingapp.UserDirectoryPage{}, err
	}
	entries := make([]billingapp.UserDirectoryEntry, len(items))
	for i, summary := range items {
		entries[i] = financeDirectoryEntry(summary)
	}
	return billingapp.UserDirectoryPage{Entries: entries, Total: total}, nil
}

func financeDirectoryEntry(summary iamdomain.UserSummary) billingapp.UserDirectoryEntry {
	return billingapp.UserDirectoryEntry{
		UserID:    summary.ID,
		Email:     summary.Email,
		Nickname:  summary.Nickname,
		Role:      summary.Role,
		GroupName: summary.GroupName,
		GroupID:   summary.GroupID,
	}
}
