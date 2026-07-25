package runtimeconfig

import (
	"bytes"
	"encoding/json"
	"io"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/donnel666/remail/internal/systemsettings/domain"
)

const (
	maxFAQsJSONBytes = 512 << 10
	maxFAQID         = 1<<53 - 1
	maxFAQWeight     = 1_000_000
)

type FAQ struct {
	ID       int64  `json:"id"`
	Question string `json:"question"`
	Answer   string `json:"answer"`
	Weight   int64  `json:"weight"`
}

func validateFAQs(value string) error {
	trimmed := strings.TrimSpace(value)
	if len(value) > maxFAQsJSONBytes || len(trimmed) < 2 || trimmed[0] != '[' {
		return domain.ErrInvalidValue
	}
	decoder := json.NewDecoder(bytes.NewBufferString(trimmed))
	decoder.DisallowUnknownFields()
	var items []FAQ
	if err := decoder.Decode(&items); err != nil {
		return domain.ErrInvalidValue
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return domain.ErrInvalidValue
	}
	ids := make(map[int64]struct{}, len(items))
	for _, item := range items {
		if item.ID <= 0 || item.ID > maxFAQID || strings.TrimSpace(item.Question) == "" || strings.TrimSpace(item.Answer) == "" ||
			utf8.RuneCountInString(item.Question) > 200 || utf8.RuneCountInString(item.Answer) > 1000 ||
			item.Weight < -maxFAQWeight || item.Weight > maxFAQWeight {
			return domain.ErrInvalidValue
		}
		if _, exists := ids[item.ID]; exists {
			return domain.ErrInvalidValue
		}
		ids[item.ID] = struct{}{}
	}
	return nil
}

func PublicFAQs(limit int) (bool, []FAQ) {
	values := Snapshot()
	enabled, err := strconv.ParseBool(strings.TrimSpace(values.String("faq_enabled", "")))
	if err != nil {
		enabled = true
	}
	if !enabled || limit <= 0 {
		return enabled, []FAQ{}
	}
	var items []FAQ
	if json.Unmarshal([]byte(values.String("faq_list", "[]")), &items) != nil {
		return enabled, []FAQ{}
	}
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].Weight != items[j].Weight {
			return items[i].Weight > items[j].Weight
		}
		return items[i].ID > items[j].ID
	})
	if len(items) > limit {
		items = items[:limit]
	}
	return enabled, items
}
