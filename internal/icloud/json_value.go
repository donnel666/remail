package icloud

import (
	"database/sql/driver"
	"encoding/json"
	"errors"
)

type iCloudJSON []byte

func (value iCloudJSON) Value() (driver.Value, error) {
	if len(value) == 0 {
		return nil, nil
	}
	if !json.Valid(value) {
		return nil, errors.New("icloud: invalid JSON value")
	}
	return string(value), nil
}

func (value *iCloudJSON) Scan(source any) error {
	var raw []byte
	switch source := source.(type) {
	case nil:
		*value = nil
		return nil
	case string:
		raw = []byte(source)
	case []byte:
		raw = source
	default:
		return errors.New("icloud: unsupported JSON database value")
	}
	if !json.Valid(raw) {
		return errors.New("icloud: invalid JSON database value")
	}
	*value = append((*value)[:0], raw...)
	return nil
}
