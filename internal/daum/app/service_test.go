package app

import (
	"context"
	"encoding/json"
	"github.com/hibiken/asynq"
	"testing"
)

type queueStub struct{ payload []byte }

func (q *queueStub) EnqueueContext(_ context.Context, t *asynq.Task, _ ...asynq.Option) (*asynq.TaskInfo, error) {
	q.payload = t.Payload()
	return nil, nil
}

func TestValidationPayloadDoesNotContainSecrets(t *testing.T) {
	q := &queueStub{}
	if err := EnqueueValidation(context.Background(), q, ValidationTaskPayload{ResourceID: 1, OwnerUserID: 2, ValidationGeneration: 3, CredentialRevision: 4}); err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	_ = json.Unmarshal(q.payload, &m)
	for _, k := range []string{"password", "refreshToken", "accessToken"} {
		if _, ok := m[k]; ok {
			t.Fatalf("secret field %s leaked", k)
		}
	}
}

func TestDecodeValidationFencesGeneration(t *testing.T) {
	_, err := DecodeValidationTask(asynq.NewTask(TaskValidate, []byte(`{"resourceId":1}`)))
	if err == nil {
		t.Fatal("expected generation validation")
	}
}
