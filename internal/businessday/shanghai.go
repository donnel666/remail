package businessday

import "time"

const dateLayout = "2006-01-02"

// Shanghai is the platform business timezone. China has no daylight-saving
// transitions in the supported business period, so a fixed zone also works in
// minimal containers without timezone data.
var Shanghai = time.FixedZone("Asia/Shanghai", 8*60*60)

// Bounds returns the Shanghai business date and its half-open UTC window.
func Bounds(now time.Time) (string, time.Time, time.Time) {
	local := now.In(Shanghai)
	start := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, Shanghai)
	return start.Format(dateLayout), start.UTC(), start.AddDate(0, 0, 1).UTC()
}

// DueSettlementBounds returns the latest business day whose configured daily
// settlement clock has passed.
func DueSettlementBounds(now time.Time, hour, minute int) (string, time.Time, time.Time) {
	local := now.In(Shanghai)
	daysBack := -1
	if local.Hour()*60+local.Minute() < hour*60+minute {
		daysBack = -2
	}
	return Bounds(local.AddDate(0, 0, daysBack))
}
