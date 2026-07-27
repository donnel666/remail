package businessday

import (
	"testing"
	"time"
)

func TestShanghaiBoundsAndSettlementClock(t *testing.T) {
	date, start, end := Bounds(time.Date(2026, 7, 26, 16, 0, 0, 0, time.UTC))
	if date != "2026-07-27" || !start.Equal(time.Date(2026, 7, 26, 16, 0, 0, 0, time.UTC)) || !end.Equal(time.Date(2026, 7, 27, 16, 0, 0, 0, time.UTC)) {
		t.Fatalf("unexpected Shanghai bounds: %s [%s, %s)", date, start, end)
	}

	date, _, _ = DueSettlementBounds(time.Date(2026, 7, 26, 21, 59, 0, 0, time.UTC), 6, 0) // July 27 05:59 Shanghai
	if date != "2026-07-25" {
		t.Fatalf("before settlement clock got %s, want 2026-07-25", date)
	}
	date, _, _ = DueSettlementBounds(time.Date(2026, 7, 26, 22, 0, 0, 0, time.UTC), 6, 0) // July 27 06:00 Shanghai
	if date != "2026-07-26" {
		t.Fatalf("at settlement clock got %s, want 2026-07-26", date)
	}
}
