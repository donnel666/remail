package app

import (
	"github.com/donnel666/remail/internal/core/domain"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestMicrosoftEnterpriseBulkSelectionPreservesScope(t *testing.T) {
	selection, err := normalizeAdminBulkSelection(AdminResourceBulkSelection{Mode: AdminResourceBulkFilter, Filter: AdminResourceBulkFilterValue{Enterprise: true}})
	require.NoError(t, err)
	require.True(t, selection.Filter.Enterprise)
	require.True(t, selection.Filter.ListFilter().Enterprise)
	enterprise, err := adminBulkFingerprint(AdminResourceBulkDelete, selection)
	require.NoError(t, err)
	selection.Filter.Enterprise = false
	personal, err := adminBulkFingerprint(AdminResourceBulkDelete, selection)
	require.NoError(t, err)
	require.NotEqual(t, personal, enterprise)
	require.NotEqual(t, adminImportFingerprint(1, false, false, domain.ImportErrorStrategySkip, []byte("same-content")), adminImportFingerprint(1, false, true, domain.ImportErrorStrategySkip, []byte("same-content")))
}
