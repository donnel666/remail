package infra

import (
	"context"
	"testing"

	"github.com/donnel666/remail/internal/daum/domain"
	"github.com/stretchr/testify/require"
)

func TestFiveFieldImportSeparatesKakaoIdentityFromPickupMailbox(t *testing.T) {
	s, _ := newDaumAsyncTestService(t)
	ctx := context.Background()
	s.ResolveBinding = func(_ context.Context, input BindingInput) (BindingResolution, error) {
		require.Equal(t, "helper@outlook.com", input.BindingAddress)
		root := uint(55)
		return BindingResolution{Status: "linked", MailboxResourceID: &root, OrderNo: "source-order"}, nil
	}
	rows, failures, err := ParseImport("account@daum.net----login-fixture----app-fixture----https://remail.test/v1/pickup?email=helper@outlook.com&token=fixture-token----account@kakao.com", "skip")
	require.NoError(t, err)
	require.Empty(t, failures)
	require.Len(t, rows, 1)
	id, _, err := s.ImportLine(ctx, 7, rows[0])
	require.NoError(t, err)
	row, err := s.GetResource(ctx, id, nil)
	require.NoError(t, err)
	require.Equal(t, "account@kakao.com", row.KakaoEmail)
	require.Equal(t, "helper@outlook.com", row.BindingAddress)
	require.Equal(t, "linked", row.BindingStatus)
	require.NoError(t, s.SetStatus(ctx, id, nil, domain.StatusDeleted))
	_, _, err = s.ImportLine(ctx, 7, domain.ImportLine{Email: "account@daum.net", AppPassword: "short-fixture"})
	require.NoError(t, err)
	row, err = s.GetResource(ctx, id, nil)
	require.NoError(t, err)
	require.Equal(t, "account@kakao.com", row.KakaoEmail)
	require.Equal(t, "helper@outlook.com", row.BindingAddress)
}

func TestUnchangedOwnerAndProtocolKeepResourceStateWithActiveAllocation(t *testing.T) {
	for _, status := range []string{domain.StatusNormal, domain.StatusDisabled} {
		t.Run(status, func(t *testing.T) {
			s, _ := newDaumAsyncTestService(t)
			ctx := context.Background()
			id, _, err := s.ImportLine(ctx, 7, domain.ImportLine{Email: "metadata@daum.net", AppPassword: "fixture"})
			require.NoError(t, err)
			require.NoError(t, s.DB.Model(&Resource{}).Where("id=?", id).Update("status", status).Error)
			require.NoError(t, s.DB.Create(&daumTestAllocation{ResourceID: id, Status: "allocated"}).Error)
			before, err := s.GetResource(ctx, id, nil)
			require.NoError(t, err)
			owner, protocol, lifetime := uint(7), "imap", true
			_, err = s.ExecuteCommand(ctx, Command{ResourceID: id, Version: before.Version, Action: "edit", OwnerID: &owner, Protocol: &protocol, LongLived: &lifetime, OperatorUserID: 7, IdempotencyKey: "metadata-only"})
			require.NoError(t, err)
			after, err := s.GetResource(ctx, id, nil)
			require.NoError(t, err)
			require.Equal(t, status, after.Status)
			require.Equal(t, before.CredentialRevision, after.CredentialRevision)
			require.Equal(t, before.ValidationGeneration, after.ValidationGeneration)
		})
	}
}
