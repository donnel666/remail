package infra

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	lotteryapp "github.com/donnel666/remail/internal/lottery/app"
	"github.com/donnel666/remail/internal/platform"
	"github.com/hibiken/asynq"
)

const (
	TypeDrawLottery      = "lottery:draw"
	lotteryQueueName     = platform.QueueDefault
	lotteryTaskTimeout   = 10 * time.Minute
	lotteryTaskUniqueTTL = 15 * time.Minute
)

type DrawTask struct {
	LotteryID uint   `json:"lotteryId"`
	Trigger   string `json:"trigger,omitempty"`
}

type Queue struct{ client *asynq.Client }

func NewQueue(client *asynq.Client) *Queue { return &Queue{client: client} }

func (q *Queue) EnqueueDraw(ctx context.Context, lotteryID uint, at *time.Time) error {
	if q == nil || q.client == nil || lotteryID == 0 {
		return fmt.Errorf("lottery queue is unavailable")
	}
	trigger := "participants"
	if at != nil {
		trigger = "time"
	}
	payload, err := json.Marshal(DrawTask{LotteryID: lotteryID, Trigger: trigger})
	if err != nil {
		return fmt.Errorf("marshal lottery draw task: %w", err)
	}
	options := []asynq.Option{
		asynq.Queue(lotteryQueueName),
		asynq.Unique(lotteryTaskUniqueTTL),
		asynq.MaxRetry(platform.BackgroundTaskMaxRetryValue()),
		asynq.Timeout(lotteryTaskTimeout),
		asynq.Retention(0),
	}
	if at != nil {
		delay := time.Until(at.UTC())
		if delay > 0 {
			options = append(options, asynq.ProcessIn(delay))
		}
	}
	_, err = q.client.EnqueueContext(ctx, asynq.NewTask(TypeDrawLottery, payload), options...)
	if errors.Is(err, asynq.ErrDuplicateTask) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("enqueue lottery draw task: %w", err)
	}
	return nil
}

var _ lotteryapp.Queue = (*Queue)(nil)
