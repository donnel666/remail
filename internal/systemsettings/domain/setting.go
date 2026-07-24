package domain

import "time"

// Setting is a generic administrator-managed system option.
//
// Values are intentionally stored as strings: callers may persist scalars,
// JSON documents, or Markdown/HTML without requiring a schema migration for
// every newly introduced option.
type Setting struct {
	Key       string
	Value     string
	CreatedAt time.Time
	UpdatedAt time.Time
}
