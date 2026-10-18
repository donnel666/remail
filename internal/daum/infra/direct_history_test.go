package infra

import (
	"context"
	"testing"

	"github.com/donnel666/remail/internal/daum/domain"
	"github.com/donnel666/remail/internal/daum/infra/protocol"
	"github.com/stretchr/testify/require"
)

func TestDaumDirectHistoryStillRejectsIncompleteOrStaleResults(t *testing.T) {
	for _, change := range []string{"incomplete", "credentials", "generation"} {
		t.Run(change, func(t *testing.T) {
			s := newHistoryTestService(t)
			ctx := context.Background()
			resource, task := historyTestResource(t, s, "direct@daum.net", domain.StatusIdentifying)
			s.Proxies = &daumNetworkProxies{t: t}
			s.Protocol = daumValidationClientStub{fetch: func(_ context.Context, credentials protocol.Credentials, request protocol.FetchRequest) (protocol.FetchResult, error) {
				require.Empty(t, credentials.ProxyURL)
				require.NoError(t, request.OnMessages([]protocol.Message{historyMessage("direct-evidence", resource.EmailAddress, s.Now())}))
				switch change {
				case "credentials":
					next := "new-fixture-app"
					require.NoError(t, s.ReplaceCredentials(ctx, resource.ID, nil, nil, &next, nil))
				case "generation":
					_, err := s.ClaimForValidation(ctx, resource.ID, nil)
					require.NoError(t, err)
				}
				return protocol.FetchResult{Complete: change != "incomplete"}, nil
			}}
			err := s.ProcessHistory(ctx, task)
			require.Error(t, err)
			if change != "incomplete" {
				require.ErrorIs(t, err, domain.ErrInvalidClaim)
			}
			var facts int64
			require.NoError(t, s.DB.Model(&historyTestUsage{}).Count(&facts).Error)
			require.Zero(t, facts)
			row, err := s.GetResource(ctx, resource.ID, nil)
			require.NoError(t, err)
			require.NotEqual(t, domain.StatusNormal, row.Status)
		})
	}
}
