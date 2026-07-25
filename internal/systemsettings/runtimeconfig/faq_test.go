package runtimeconfig

import (
	"encoding/json"
	"testing"

	"github.com/donnel666/remail/internal/systemsettings/domain"
	"github.com/stretchr/testify/require"
)

func TestPublicFAQsValidateSortAndRespectSwitch(t *testing.T) {
	payload := `[{"id":1,"question":"Older","answer":"A","weight":5},{"id":3,"question":"Newest","answer":"C","weight":5},{"id":2,"question":"Pinned","answer":"B","weight":10}]`
	require.NoError(t, Validate("faq_list", payload))
	require.ErrorIs(t, Validate("faq_list", `[{"id":1,"question":"","answer":"A","weight":0}]`), domain.ErrInvalidValue)
	require.ErrorIs(t, Validate("faq_list", `[{"id":1,"question":"Q","answer":"A","weight":1000001}]`), domain.ErrInvalidValue)

	Replace([]domain.Setting{{Key: "faq_enabled", Value: "true"}, {Key: "faq_list", Value: payload}})
	t.Cleanup(func() { Replace(nil) })
	enabled, items := PublicFAQs(2)
	require.True(t, enabled)
	require.Equal(t, []int64{2, 3}, []int64{items[0].ID, items[1].ID})

	Set("faq_enabled", "false")
	enabled, items = PublicFAQs(20)
	require.False(t, enabled)
	require.Empty(t, items)
}

func TestValidateFAQsDoesNotImposeAnEntryCountLimit(t *testing.T) {
	items := make([]FAQ, 51)
	for i := range items {
		items[i] = FAQ{ID: int64(i + 1), Question: "Q", Answer: "A"}
	}
	payload, err := json.Marshal(items)
	require.NoError(t, err)
	require.NoError(t, Validate("faq_list", string(payload)))
}
