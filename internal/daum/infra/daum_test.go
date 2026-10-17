package infra

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/donnel666/remail/internal/daum/app"
	"github.com/donnel666/remail/internal/daum/domain"
	"github.com/donnel666/remail/internal/daum/infra/protocol"
	"github.com/stretchr/testify/require"
)

func TestFixedAliasesDuplicateImportAndRestoreKeepOptionalCredentials(t *testing.T) {
	s, _ := newDaumAsyncTestService(t)
	ctx := context.Background()
	line := domain.ImportLine{Email: "one@hanmail.net", Password: "login-secret", AppPassword: "app-secret", BindingAddress: "aux@example.test", PickupURL: "https://remail.test/pickup?email=aux%40example.test&token=pickup-secret"}
	id, _, err := s.ImportLine(ctx, 7, line)
	require.NoError(t, err)
	aliases, err := s.ListAliases(ctx, id)
	require.NoError(t, err)
	require.Len(t, aliases, 2)
	require.Equal(t, uint(1), aliases[0].OwnerUserID)
	id2, outcome, err := s.ImportLine(ctx, 7, domain.ImportLine{Email: "one@hanmail2.net", AppPassword: "replacement"})
	require.NoError(t, err)
	require.Equal(t, id, id2)
	require.Equal(t, "skipped", outcome)
	_, _, err = s.ImportLine(ctx, 8, line)
	require.ErrorIs(t, err, domain.ErrResourceConflict)
	require.NoError(t, s.SetStatus(ctx, id, nil, domain.StatusDeleted))
	_, outcome, err = s.ImportLine(ctx, 7, domain.ImportLine{Email: "one@daum.net", AppPassword: "new-app"})
	require.NoError(t, err)
	require.Equal(t, "restored", outcome)
	var row Resource
	require.NoError(t, s.DB.First(&row, id).Error)
	require.Equal(t, "login-secret", row.Password)
	require.Equal(t, "new-app", row.AppPassword)
	var binding BindingMailbox
	require.NoError(t, s.DB.Where("resource_id = ?", id).Take(&binding).Error)
	require.Equal(t, line.PickupURL, binding.PickupURL)
	public, err := s.GetResource(ctx, id, nil)
	require.NoError(t, err)
	require.Empty(t, public.Password)
	require.Empty(t, public.AppPassword)
	require.True(t, public.PasswordConfigured)
	require.True(t, public.AppPasswordConfigured)
}
func TestIndependentKakaoIdentityDoesNotOverwriteAccountValidation(t *testing.T) {
	s, _ := newDaumAsyncTestService(t)
	ctx := context.Background()
	id, _, err := s.ImportLine(ctx, 7, domain.ImportLine{Email: "one@daum.net", AppPassword: "app", KakaoEmail: "one@kakao.com", PickupURL: "https://remail.test/v1/pickup?email=other%40outlook.com&token=synthetic-secret", BindingAddress: "other@outlook.com"})
	require.NoError(t, err)
	require.NoError(t, s.ProcessValidation(ctx, app.ValidationTaskPayload{ResourceID: id, OwnerUserID: 7, ValidationGeneration: 1, CredentialRevision: 1}))
	row, err := s.GetResource(ctx, id, nil)
	require.NoError(t, err)
	require.Equal(t, domain.StatusIdentifying, row.Status)
	require.Equal(t, "unlinked", row.BindingStatus)
	require.Equal(t, "one@kakao.com", row.KakaoEmail)
	require.Equal(t, "other@outlook.com", row.BindingAddress)
}
func TestValidationUsesAppPasswordAndFencesConcurrentReplacement(t *testing.T) {
	s, _ := newDaumAsyncTestService(t)
	ctx := context.Background()
	id, _, err := s.ImportLine(ctx, 7, domain.ImportLine{Email: "one@daum.net", Password: "login", AppPassword: "app"})
	require.NoError(t, err)
	s.Protocol = daumValidationClientStub{validate: func(_ context.Context, c protocol.Credentials) error {
		require.Equal(t, "app", c.AppPassword)
		replacement := "new-app"
		require.NoError(t, s.ReplaceCredentials(ctx, id, nil, nil, &replacement, nil))
		return nil
	}}
	require.ErrorIs(t, s.ProcessValidation(ctx, app.ValidationTaskPayload{ResourceID: id, OwnerUserID: 7, ValidationGeneration: 1, CredentialRevision: 1}), domain.ErrInvalidClaim)
	row, err := s.GetResource(ctx, id, nil)
	require.NoError(t, err)
	require.Equal(t, domain.StatusPending, row.Status)
	require.EqualValues(t, 2, row.CredentialRevision)
}
func TestCredentialCommandsAreIdempotentAndNeverReturnSecrets(t *testing.T) {
	s, _ := newDaumAsyncTestService(t)
	ctx := context.Background()
	id, _, err := s.ImportLine(ctx, 7, domain.ImportLine{Email: "one@daum.net", AppPassword: "app"})
	require.NoError(t, err)
	secret := "secret-new-app"
	cmd := Command{ResourceID: id, Version: 1, Action: "credentials", AppPassword: &secret, OperatorUserID: 7, IdempotencyKey: "same"}
	first, err := s.ExecuteCommand(ctx, cmd)
	require.NoError(t, err)
	second, err := s.ExecuteCommand(ctx, cmd)
	require.NoError(t, err)
	require.True(t, second.Reused)
	require.Equal(t, first.Version, second.Version)
	raw, err := json.Marshal(second)
	require.NoError(t, err)
	require.NotContains(t, string(raw), secret)
	secret = "changed"
	_, err = s.ExecuteCommand(ctx, cmd)
	require.ErrorIs(t, err, domain.ErrImportConflict)
}
func TestHistoricalUsageSeparatesAllThreeAddresses(t *testing.T) {
	scope := historyProjectScope{ProjectID: 1, ProductID: 2, LooseMatch: true, Rules: []historyRule{{Type: "recipient", Pattern: "exact"}, {Type: "sender", Pattern: "sender@example.test"}}}
	accumulator := newHistoryAccumulator(Resource{ID: 7, EmailAddress: "one@daum.net"}, []historyProjectScope{scope}, time.Now())
	for _, address := range []string{"one@daum.net", "one@hanmail.net", "one@hanmail2.net"} {
		require.NoError(t, accumulator.add([]protocol.Message{{ID: address, Sender: protocol.Address{Address: "sender@example.test"}, ToList: []protocol.Address{{Address: address}}, OriginalToCount: 1}}))
	}
	require.Len(t, accumulator.matches, 3)
	require.Equal(t, "one@hanmail.net", accumulator.matches[1].Email)
	require.NoError(t, accumulator.add([]protocol.Message{{ID: "ambiguous", Sender: protocol.Address{Address: "sender@example.test"}, ToList: []protocol.Address{{Address: "one@hanmail.net"}, {Address: "one@hanmail2.net"}}, OriginalToCount: 2}}))
	require.Len(t, accumulator.matches, 3)
}

func TestDaumImportUses256BucketsAcrossIDBoundary(t *testing.T) {
	s, _ := newDaumAsyncTestService(t)
	require.NoError(t, s.DB.Create(&resourceRoot{ID: 510, Type: "daum", OwnerUserID: 7, Version: 1}).Error)
	for _, test := range []struct {
		email  string
		id     uint
		bucket uint16
	}{{"last@daum.net", 511, 255}, {"wrapped@daum.net", 512, 0}, {"next@daum.net", 513, 1}} {
		id, _, err := s.ImportLine(context.Background(), 7, domain.ImportLine{Email: test.email, AppPassword: "fixture-app"})
		require.NoError(t, err)
		require.Equal(t, test.id, id)
		var row Resource
		require.NoError(t, s.DB.First(&row, id).Error)
		require.Equal(t, test.bucket, row.AllocBucket)
	}
}
func TestFetchRejectsStaleCredentialsAndCrossAccountRecipient(t *testing.T) {
	s, _ := newDaumAsyncTestService(t)
	ctx := context.Background()
	id, _, err := s.ImportLine(ctx, 7, domain.ImportLine{Email: "one@daum.net", AppPassword: "app"})
	require.NoError(t, err)
	require.NoError(t, s.DB.Model(&Resource{}).Where("id = ?", id).Update("status", domain.StatusNormal).Error)
	_, err = s.FetchMailbox(ctx, id, 2, protocol.FetchRequest{})
	require.ErrorIs(t, err, domain.ErrInvalidClaim)
	_, err = s.FetchMailbox(ctx, id, 1, protocol.FetchRequest{Recipient: "another@hanmail.net"})
	require.ErrorIs(t, err, domain.ErrInvalidResource)
	s.Protocol = daumValidationClientStub{fetch: func(_ context.Context, _ protocol.Credentials, request protocol.FetchRequest) (protocol.FetchResult, error) {
		require.Equal(t, "one@hanmail2.net", request.Recipient)
		require.NoError(t, s.SetStatus(ctx, id, nil, domain.StatusDisabled))
		return protocol.FetchResult{Complete: true}, nil
	}}
	_, err = s.FetchMailbox(ctx, id, 1, protocol.FetchRequest{Recipient: "one@hanmail2.net"})
	require.ErrorIs(t, err, domain.ErrInvalidClaim)
}
