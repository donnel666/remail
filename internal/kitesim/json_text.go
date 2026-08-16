package kitesim

import (
	"database/sql/driver"
	"encoding/json"
	"errors"
)

type jsonText string

func (value jsonText) Value() (driver.Value, error) {
	if len(value) == 0 {
		return nil, nil
	}
	if !json.Valid([]byte(value)) {
		return nil, errors.New("kitesim: invalid JSON value")
	}
	return string(value), nil
}
