package runtimeconfig

import (
	"bytes"
	"encoding/json"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/donnel666/remail/internal/systemsettings/domain"
)

const (
	maxAnnouncementContentBytes = 1 << 20
	maxAnnouncementsJSONBytes   = 128 << 20
	maxSafeAnnouncementID       = 1<<53 - 1
)

type Announcement struct {
	ID        int64  `json:"id"`
	Title     string `json:"title"`
	Content   string `json:"content"`
	Type      string `json:"type"`
	StartTime string `json:"startTime"`
	EndTime   string `json:"endTime"`
	Enabled   bool   `json:"enabled"`
}

func validateAnnouncements(value string) error {
	trimmed := strings.TrimSpace(value)
	if len(value) > maxAnnouncementsJSONBytes || len(trimmed) < 2 || trimmed[0] != '[' {
		return domain.ErrInvalidValue
	}
	decoder := json.NewDecoder(bytes.NewBufferString(trimmed))
	decoder.DisallowUnknownFields()
	var announcements []Announcement
	if err := decoder.Decode(&announcements); err != nil {
		return domain.ErrInvalidValue
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF || len(announcements) > 100 {
		return domain.ErrInvalidValue
	}
	ids := make(map[int64]struct{}, len(announcements))
	for _, announcement := range announcements {
		if announcement.ID <= 0 || announcement.ID > maxSafeAnnouncementID || strings.TrimSpace(announcement.Title) == "" || strings.TrimSpace(announcement.Content) == "" ||
			utf8.RuneCountInString(announcement.Title) > 200 || len(announcement.Content) > maxAnnouncementContentBytes ||
			!validAnnouncementType(announcement.Type) {
			return domain.ErrInvalidValue
		}
		if _, exists := ids[announcement.ID]; exists {
			return domain.ErrInvalidValue
		}
		ids[announcement.ID] = struct{}{}
		start, startOK := parseAnnouncementTime(announcement.StartTime)
		end, endOK := parseAnnouncementTime(announcement.EndTime)
		if (announcement.StartTime != "" && !startOK) || (announcement.EndTime != "" && !endOK) ||
			(startOK && endOK && end.Before(start)) {
			return domain.ErrInvalidValue
		}
	}
	return nil
}

func ActiveAnnouncements(now time.Time, limit int) []Announcement {
	values := Snapshot()
	enabled, err := strconv.ParseBool(strings.TrimSpace(values.String("announcement_enabled", "")))
	if limit <= 0 || (err == nil && !enabled) {
		return []Announcement{}
	}
	var announcements []Announcement
	if json.Unmarshal([]byte(values.String("announcements", "[]")), &announcements) != nil {
		return []Announcement{}
	}
	active := make([]Announcement, 0, min(limit, len(announcements)))
	for _, announcement := range announcements {
		if !announcement.Enabled {
			continue
		}
		start, startOK := parseAnnouncementTime(announcement.StartTime)
		end, endOK := parseAnnouncementTime(announcement.EndTime)
		if (startOK && start.After(now)) || (endOK && !end.After(now)) {
			continue
		}
		active = append(active, announcement)
	}
	sort.SliceStable(active, func(i, j int) bool {
		left, leftOK := parseAnnouncementTime(active[i].StartTime)
		right, rightOK := parseAnnouncementTime(active[j].StartTime)
		if leftOK && rightOK && !left.Equal(right) {
			return left.After(right)
		}
		if leftOK != rightOK {
			return leftOK
		}
		return active[i].ID > active[j].ID
	})
	if len(active) > limit {
		active = active[:limit]
	}
	return active
}

func validAnnouncementType(value string) bool {
	switch value {
	case "default", "ongoing", "success", "warning", "error":
		return true
	default:
		return false
	}
}

func parseAnnouncementTime(value string) (time.Time, bool) {
	if value == "" {
		return time.Time{}, false
	}
	parsed, err := time.Parse(time.RFC3339, value)
	return parsed, err == nil
}
