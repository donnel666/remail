package app

import (
	"context"
	"errors"
	"testing"

	"github.com/donnel666/remail/internal/trade/domain"
	"github.com/stretchr/testify/require"
)

type projectDisplayPortSpy struct {
	calls    int
	ids      []uint
	displays map[uint]ProjectDisplay
	err      error
}

func (s *projectDisplayPortSpy) ProjectDisplays(_ context.Context, projectIDs []uint) (map[uint]ProjectDisplay, error) {
	s.calls++
	s.ids = append([]uint(nil), projectIDs...)
	return s.displays, s.err
}

func TestAttachProjectDisplaysUsesOneDeduplicatedBatchAndFallsBackToEmpty(t *testing.T) {
	port := &projectDisplayPortSpy{displays: map[uint]ProjectDisplay{
		7: {Name: "Project Seven", LogoURL: "/v1/projects/logos/seven"},
		8: {Name: "Project Eight"},
	}}
	uc := &UseCase{projectDisplays: port}
	results := []CheckoutResult{
		{Order: domain.Order{ProjectID: 7}},
		{Order: domain.Order{ProjectID: 7}},
		{Order: domain.Order{ProjectID: 8}},
		{Order: domain.Order{ProjectID: 9}}, // Missing/deleted project.
		{Order: domain.Order{}},
	}

	require.NoError(t, uc.attachProjectDisplays(context.Background(), results))
	require.Equal(t, 1, port.calls)
	require.Equal(t, []uint{7, 8, 9}, port.ids)
	require.Equal(t, "Project Seven", results[0].ProjectName)
	require.Equal(t, "/v1/projects/logos/seven", results[0].ProjectLogoURL)
	require.Equal(t, "Project Seven", results[1].ProjectName)
	require.Equal(t, "/v1/projects/logos/seven", results[1].ProjectLogoURL)
	require.Equal(t, "Project Eight", results[2].ProjectName)
	require.Empty(t, results[2].ProjectLogoURL)
	require.Empty(t, results[3].ProjectName)
	require.Empty(t, results[3].ProjectLogoURL)
	require.Empty(t, results[4].ProjectName)
	require.Empty(t, results[4].ProjectLogoURL)
}

func TestAttachProjectDisplaysPropagatesBatchLookupError(t *testing.T) {
	wantErr := errors.New("project display lookup failed")
	port := &projectDisplayPortSpy{err: wantErr}
	uc := &UseCase{projectDisplays: port}

	err := uc.attachProjectDisplays(context.Background(), []CheckoutResult{{
		Order: domain.Order{ProjectID: 7},
	}})

	require.ErrorIs(t, err, wantErr)
	require.Equal(t, 1, port.calls)
}
