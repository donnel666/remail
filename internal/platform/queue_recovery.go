package platform

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/redis/go-redis/v9"
)

const queueRecoveryScanCount = 256

// LiveTaskResourceIDs finds live tasks for only the resource IDs being
// recovered. Redis task hashes are scanned in batches, so a large queue does
// not make recovery stop at an arbitrary task-count ceiling.
func LiveTaskResourceIDs(
	ctx context.Context,
	client redis.UniversalClient,
	queue, taskType string,
	candidates map[uint]struct{},
) (map[uint]struct{}, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	live := make(map[uint]struct{})
	if len(candidates) == 0 {
		return live, nil
	}

	pattern := "asynq:{" + queue + "}:t:*"
	var cursor uint64
	for {
		keys, next, err := client.Scan(ctx, cursor, pattern, queueRecoveryScanCount).Result()
		if err != nil {
			return nil, fmt.Errorf("scan recovery queue tasks: %w", err)
		}
		if len(keys) > 0 {
			pipe := client.Pipeline()
			commands := make(map[string]*redis.SliceCmd, len(keys))
			for _, key := range keys {
				commands[key] = pipe.HMGet(ctx, key, "msg", "state")
			}
			if _, err := pipe.Exec(ctx); err != nil {
				return nil, fmt.Errorf("load recovery queue tasks: %w", err)
			}
			for _, command := range commands {
				values, err := command.Result()
				if err != nil {
					return nil, fmt.Errorf("read recovery queue task: %w", err)
				}
				if len(values) != 2 || !isLiveRecoveryTaskState(values[1]) {
					continue
				}
				taskTypeValue, payload, ok := decodeRecoveryTaskMessage(values[0])
				if !ok || taskTypeValue != taskType {
					continue
				}
				var task struct {
					ResourceID uint `json:"resourceId"`
				}
				if json.Unmarshal(payload, &task) != nil || task.ResourceID == 0 {
					continue
				}
				if _, wanted := candidates[task.ResourceID]; wanted {
					live[task.ResourceID] = struct{}{}
				}
			}
		}
		if len(live) == len(candidates) || next == 0 {
			return live, nil
		}
		cursor = next
	}
}

func isLiveRecoveryTaskState(value any) bool {
	state, ok := value.(string)
	if !ok {
		if bytes, ok := value.([]byte); ok {
			state = string(bytes)
		} else {
			return false
		}
	}
	switch state {
	case "pending", "active", "scheduled", "retry":
		return true
	default:
		return false
	}
}

func decodeRecoveryTaskMessage(value any) (string, []byte, bool) {
	var raw []byte
	switch typed := value.(type) {
	case string:
		raw = []byte(typed)
	case []byte:
		raw = typed
	default:
		return "", nil, false
	}
	position := 0
	var taskType string
	var payload []byte
	for position < len(raw) {
		tag, ok := readRecoveryVarint(raw, &position)
		if !ok {
			return "", nil, false
		}
		field, wire := tag>>3, tag&7
		switch wire {
		case 0:
			if _, ok := readRecoveryVarint(raw, &position); !ok {
				return "", nil, false
			}
		case 1:
			if position+8 > len(raw) {
				return "", nil, false
			}
			position += 8
		case 2:
			length, ok := readRecoveryVarint(raw, &position)
			if !ok || length > uint64(len(raw)-position) {
				return "", nil, false
			}
			value := raw[position : position+int(length)]
			position += int(length)
			switch field {
			case 1:
				taskType = string(value)
			case 2:
				payload = append([]byte(nil), value...)
			}
		case 5:
			if position+4 > len(raw) {
				return "", nil, false
			}
			position += 4
		default:
			return "", nil, false
		}
	}
	return taskType, payload, taskType != "" && payload != nil
}

func readRecoveryVarint(raw []byte, position *int) (uint64, bool) {
	var value uint64
	for shift := uint(0); *position < len(raw) && shift < 64; shift += 7 {
		current := raw[*position]
		*position = *position + 1
		value |= uint64(current&0x7f) << shift
		if current&0x80 == 0 {
			return value, true
		}
	}
	return 0, false
}
