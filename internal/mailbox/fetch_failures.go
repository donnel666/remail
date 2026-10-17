package mailbox

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

const FetchAttempts = 3
const FetchFailureThreshold = int64(3)
const FetchFailureTTL = time.Hour

func FetchFailureKey(resourceType string, resourceID uint) string {
	return fmt.Sprintf("mailmatch:%s:fetch_failures:%d", resourceType, resourceID)
}

func IncrementFetchFailure(ctx context.Context, client redis.UniversalClient, key string) (int64, error) {
	if client == nil || key == "" {
		return 0, nil
	}
	var increment *redis.IntCmd
	_, err := client.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
		increment = pipe.Incr(ctx, key)
		pipe.Expire(ctx, key, FetchFailureTTL)
		return nil
	})
	if err != nil {
		return 0, err
	}
	return increment.Val(), nil
}

func ClearFetchFailures(ctx context.Context, client redis.UniversalClient, key string) error {
	if client == nil || key == "" {
		return nil
	}
	return client.Del(ctx, key).Err()
}
