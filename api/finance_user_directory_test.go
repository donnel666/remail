package api

import (
	"context"
	"testing"

	billingapp "github.com/donnel666/remail/internal/billing/app"
	iamdomain "github.com/donnel666/remail/internal/iam/domain"
)

type fakeUserSummarySource struct {
	lookup    map[uint]iamdomain.UserSummary
	list      []iamdomain.UserSummary
	total     int
	gotSearch string
	gotOffset int
	gotLimit  int
}

func (f *fakeUserSummarySource) LookupUserSummaries(_ context.Context, ids []uint) (map[uint]iamdomain.UserSummary, error) {
	out := map[uint]iamdomain.UserSummary{}
	for _, id := range ids {
		if s, ok := f.lookup[id]; ok {
			out[id] = s
		}
	}
	return out, nil
}

func (f *fakeUserSummarySource) ListUserSummaries(_ context.Context, search string, offset, limit int) ([]iamdomain.UserSummary, int, error) {
	f.gotSearch, f.gotOffset, f.gotLimit = search, offset, limit
	return f.list, f.total, nil
}

func TestFinanceUserDirectoryLookupMapsFields(t *testing.T) {
	src := &fakeUserSummarySource{lookup: map[uint]iamdomain.UserSummary{
		7: {ID: 7, Email: "a@b.com", Nickname: "Nick", Role: "supplier", GroupID: 3, GroupName: "VIP"},
	}}
	d := financeUserDirectory{users: src}

	got, err := d.LookupUsers(context.Background(), []uint{7})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	e, ok := got[7]
	if !ok {
		t.Fatalf("user 7 missing from result")
	}
	if e.UserID != 7 || e.Email != "a@b.com" || e.Nickname != "Nick" ||
		e.Role != "supplier" || e.GroupID != 3 || e.GroupName != "VIP" {
		t.Fatalf("field mapping wrong: %+v", e)
	}
}

func TestFinanceUserDirectoryLookupEmptyIDsSkipsSource(t *testing.T) {
	d := financeUserDirectory{users: &fakeUserSummarySource{}}
	got, err := d.LookupUsers(context.Background(), nil)
	if err != nil || len(got) != 0 {
		t.Fatalf("expected empty result, got %v (err %v)", got, err)
	}
}

func TestFinanceUserDirectoryListPassesQueryAndTotal(t *testing.T) {
	src := &fakeUserSummarySource{
		list:  []iamdomain.UserSummary{{ID: 1, Email: "x@y.z", GroupID: 2, GroupName: "G"}},
		total: 42,
	}
	d := financeUserDirectory{users: src}

	page, err := d.ListUsers(context.Background(), billingapp.UserDirectoryQuery{Search: "foo", Offset: 20, Limit: 10})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if page.Total != 42 {
		t.Fatalf("total not passed through: %d", page.Total)
	}
	if len(page.Entries) != 1 || page.Entries[0].UserID != 1 || page.Entries[0].GroupName != "G" {
		t.Fatalf("entry mapping wrong: %+v", page.Entries)
	}
	if src.gotSearch != "foo" || src.gotOffset != 20 || src.gotLimit != 10 {
		t.Fatalf("query not forwarded: %q offset=%d limit=%d", src.gotSearch, src.gotOffset, src.gotLimit)
	}
}

func TestFinanceUserDirectoryNilSourceSafe(t *testing.T) {
	var d financeUserDirectory // nil source
	if _, err := d.LookupUsers(context.Background(), []uint{1}); err != nil {
		t.Fatalf("lookup with nil source: %v", err)
	}
	page, err := d.ListUsers(context.Background(), billingapp.UserDirectoryQuery{})
	if err != nil || page.Total != 0 || len(page.Entries) != 0 {
		t.Fatalf("nil source not safe: %+v (err %v)", page, err)
	}
}
