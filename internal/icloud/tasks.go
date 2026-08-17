package icloud

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/donnel666/remail/internal/platform"
	"github.com/hibiken/asynq"
)

const (
	typeICloudImport               = "icloud:resource_import"
	typeICloudImportDispatcher     = "icloud:resource_import_dispatcher"
	typeICloudValidation           = "icloud:resource_validation"
	typeICloudValidationDispatcher = "icloud:resource_validation_dispatcher"
	typeICloudProvision            = "icloud:resource_provision"
	typeICloudProvisionDispatcher  = "icloud:resource_provision_dispatcher"
	typeICloudOnboarding           = "icloud:account_onboarding"
	typeICloudOnboardingDispatcher = "icloud:account_onboarding_dispatcher"

	iCloudDispatcherPeriod         = 30 * time.Second
	iCloudDispatcherTaskTimeout    = 30 * time.Second
	iCloudValidationTaskTimeout    = 3 * time.Minute
	iCloudValidationActivationWait = time.Second
	iCloudProvisionTaskTimeout     = 3 * time.Minute
	iCloudOnboardingTaskTimeout    = 2 * time.Minute
	iCloudOnboardingActivationWait = time.Second
)

func (s *Service) ScheduleICloudOnboardingDispatcher(ctx context.Context, delay time.Duration) error {
	if s == nil || s.queue == nil {
		return ErrICloudOnboardingTemporary
	}
	if delay < 0 {
		delay = 0
	}
	options := []asynq.Option{
		asynq.Queue(platform.QueueBackgroundICloudValidation), asynq.Unique(iCloudDispatcherTaskTimeout + delay),
		asynq.MaxRetry(0), asynq.Timeout(iCloudDispatcherTaskTimeout), asynq.Retention(0),
	}
	if delay > 0 {
		options = append(options, asynq.ProcessIn(delay))
	}
	_, err := s.queue.EnqueueContext(ctx, asynq.NewTask(typeICloudOnboardingDispatcher, nil), options...)
	if errors.Is(err, asynq.ErrDuplicateTask) {
		return nil
	}
	if err != nil {
		return ErrICloudOnboardingTemporary
	}
	return nil
}

func (s *Service) enqueueICloudOnboardingTask(ctx context.Context, task iCloudOnboardingTask) (bool, error) {
	if s == nil || s.queue == nil || task.TaskID == 0 || task.Generation == 0 {
		return false, ErrICloudOnboardingTemporary
	}
	payload, err := json.Marshal(task)
	if err != nil {
		return false, ErrICloudOnboardingTemporary
	}
	_, err = s.queue.EnqueueContext(ctx, asynq.NewTask(typeICloudOnboarding, payload),
		asynq.Queue(platform.QueueBackgroundICloudValidation),
		asynq.Unique(iCloudOnboardingTaskTimeout+iCloudOnboardingActivationWait),
		asynq.MaxRetry(platform.BackgroundTaskMaxRetryValue()), asynq.Timeout(iCloudOnboardingTaskTimeout),
		asynq.ProcessIn(iCloudOnboardingActivationWait), asynq.Retention(0),
	)
	if errors.Is(err, asynq.ErrDuplicateTask) {
		return false, nil
	}
	if err != nil {
		return false, ErrICloudOnboardingTemporary
	}
	return true, nil
}

func (s *Service) ScheduleICloudImportDispatcher(ctx context.Context, delay time.Duration) error {
	if s == nil || s.queue == nil {
		return ErrICloudImportDependency
	}
	if delay < 0 {
		delay = 0
	}
	options := []asynq.Option{
		asynq.Queue(platform.QueueDefault), asynq.Unique(iCloudDispatcherTaskTimeout + delay),
		asynq.MaxRetry(0), asynq.Timeout(iCloudDispatcherTaskTimeout), asynq.Retention(0),
	}
	if delay > 0 {
		options = append(options, asynq.ProcessIn(delay))
	}
	_, err := s.queue.EnqueueContext(ctx, asynq.NewTask(typeICloudImportDispatcher, nil), options...)
	if errors.Is(err, asynq.ErrDuplicateTask) {
		return nil
	}
	if err != nil {
		return ErrICloudImportTemporary
	}
	return nil
}

func (s *Service) ScheduleICloudValidationDispatcher(ctx context.Context, delay time.Duration) error {
	if s == nil || s.queue == nil {
		return ErrICloudValidationTemp
	}
	if delay < 0 {
		delay = 0
	}
	options := []asynq.Option{
		asynq.Queue(platform.QueueBackgroundICloudValidation), asynq.Unique(iCloudDispatcherTaskTimeout + delay),
		asynq.MaxRetry(0), asynq.Timeout(iCloudDispatcherTaskTimeout), asynq.Retention(0),
	}
	if delay > 0 {
		options = append(options, asynq.ProcessIn(delay))
	}
	_, err := s.queue.EnqueueContext(ctx, asynq.NewTask(typeICloudValidationDispatcher, nil), options...)
	if errors.Is(err, asynq.ErrDuplicateTask) {
		return nil
	}
	if err != nil {
		return ErrICloudValidationTemp
	}
	return nil
}

func (s *Service) ScheduleICloudProvisionDispatcher(ctx context.Context, delay time.Duration) error {
	if s == nil || s.queue == nil {
		return ErrICloudValidationTemp
	}
	if delay < 0 {
		delay = 0
	}
	options := []asynq.Option{
		asynq.Queue(platform.QueueBackgroundICloudValidation), asynq.Unique(iCloudDispatcherTaskTimeout + delay),
		asynq.MaxRetry(0), asynq.Timeout(iCloudDispatcherTaskTimeout), asynq.Retention(0),
	}
	if delay > 0 {
		options = append(options, asynq.ProcessIn(delay))
	}
	_, err := s.queue.EnqueueContext(ctx, asynq.NewTask(typeICloudProvisionDispatcher, nil), options...)
	if errors.Is(err, asynq.ErrDuplicateTask) {
		return nil
	}
	if err != nil {
		return ErrICloudValidationTemp
	}
	return nil
}

func (s *Service) ScheduleICloudProvision(ctx context.Context, resourceID uint, delay time.Duration) error {
	if s == nil || s.queue == nil || resourceID == 0 {
		return ErrICloudValidationTemp
	}
	payload, err := json.Marshal(iCloudProvisionTask{ResourceID: resourceID})
	if err != nil {
		return ErrICloudValidationTemp
	}
	options := []asynq.Option{
		asynq.Queue(platform.QueueBackgroundICloudValidation), asynq.Unique(iCloudProvisionTaskTimeout + delay),
		asynq.MaxRetry(platform.BackgroundTaskMaxRetryValue()), asynq.Timeout(iCloudProvisionTaskTimeout), asynq.Retention(0),
	}
	if delay > 0 {
		options = append(options, asynq.ProcessIn(delay))
	}
	_, err = s.queue.EnqueueContext(ctx, asynq.NewTask(typeICloudProvision, payload), options...)
	if errors.Is(err, asynq.ErrDuplicateTask) {
		return nil
	}
	if err != nil {
		return ErrICloudValidationTemp
	}
	return nil
}

func (s *Service) enqueueICloudValidation(ctx context.Context, task iCloudValidationTask) (bool, error) {
	if s == nil || s.queue == nil || task.ResourceID == 0 || task.OwnerUserID == 0 || task.ValidationGeneration == 0 || task.ExpectedCredentialRevision == 0 {
		return false, ErrICloudValidationTemp
	}
	payload, err := json.Marshal(task)
	if err != nil {
		return false, ErrICloudValidationTemp
	}
	_, err = s.queue.EnqueueContext(ctx, asynq.NewTask(typeICloudValidation, payload),
		asynq.Queue(platform.QueueBackgroundICloudValidation),
		asynq.Unique(iCloudValidationTaskTimeout+iCloudValidationActivationWait),
		asynq.MaxRetry(platform.BackgroundTaskMaxRetryValue()), asynq.Timeout(iCloudValidationTaskTimeout),
		asynq.ProcessIn(iCloudValidationActivationWait), asynq.Retention(0),
	)
	if errors.Is(err, asynq.ErrDuplicateTask) {
		return false, nil
	}
	if err != nil {
		return false, ErrICloudValidationTemp
	}
	return true, nil
}

// RegisterTaskHandlers keeps iCloud workers isolated from the stable Core
// handlers. The periodic seeds recover durable pending work after Redis or a
// worker process becomes temporarily unavailable.
func RegisterTaskHandlers(mux *asynq.ServeMux, service *Service) func(context.Context) {
	if mux == nil || service == nil {
		return func(context.Context) {}
	}
	mux.HandleFunc(typeICloudImportDispatcher, func(ctx context.Context, _ *asynq.Task) error {
		_ = service.DispatchICloudImports(ctx, 100)
		return nil
	})
	mux.HandleFunc(typeICloudValidationDispatcher, func(ctx context.Context, _ *asynq.Task) error {
		queueName, _ := asynq.GetQueueName(ctx)
		return handleICloudValidationDispatcher(ctx, service, queueName)
	})
	mux.HandleFunc(typeICloudProvisionDispatcher, func(ctx context.Context, _ *asynq.Task) error {
		return service.DispatchICloudProvisions(ctx, iCloudProvisionBatchLimit)
	})
	mux.HandleFunc(typeICloudOnboardingDispatcher, func(ctx context.Context, _ *asynq.Task) error {
		_ = service.DispatchICloudOnboardingTasks(ctx, 100)
		return nil
	})
	mux.HandleFunc(typeICloudOnboarding, func(ctx context.Context, task *asynq.Task) error {
		payload, err := decodeICloudOnboardingTask(task)
		if err != nil {
			return err
		}
		release, admitted := platform.AcquireBackgroundExecution(ctx, service.backgroundExecution)
		if !admitted {
			if platform.BackgroundTaskHasRetryHeadroom(ctx) {
				return platform.ErrBackgroundExecutionDeferred
			}
			_ = service.ReleaseICloudOnboardingTask(context.WithoutCancel(ctx), payload, "Onboarding execution was deferred; dispatcher will retry.")
			return nil
		}
		defer release()
		if err := service.ProcessICloudOnboardingTask(ctx, payload); err != nil {
			if platform.BackgroundTaskHasRetryHeadroom(ctx) {
				return err
			}
			releaseCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer cancel()
			_ = service.ReleaseICloudOnboardingTask(releaseCtx, payload, "Onboarding infrastructure is temporarily unavailable; dispatcher will retry.")
			_ = service.ScheduleICloudOnboardingDispatcher(releaseCtx, time.Second)
			return nil
		}
		_ = service.ScheduleICloudOnboardingDispatcher(context.WithoutCancel(ctx), 0)
		return nil
	})
	mux.HandleFunc(typeICloudProvision, func(ctx context.Context, task *asynq.Task) error {
		var payload iCloudProvisionTask
		if task == nil || json.Unmarshal(task.Payload(), &payload) != nil || payload.ResourceID == 0 {
			return fmt.Errorf("decode iCloud provision task: %w", asynq.SkipRetry)
		}
		release, admitted := platform.AcquireBackgroundExecution(ctx, service.backgroundExecution)
		if !admitted {
			if platform.BackgroundTaskHasRetryHeadroom(ctx) {
				return platform.ErrBackgroundExecutionDeferred
			}
			return nil
		}
		defer release()
		if err := service.ProcessICloudProvision(ctx, payload); err != nil {
			if platform.BackgroundTaskHasRetryHeadroom(ctx) {
				return err
			}
			_ = service.ScheduleICloudProvisionDispatcher(context.WithoutCancel(ctx), time.Second)
			return nil
		}
		return service.ScheduleICloudProvisionDispatcher(context.WithoutCancel(ctx), time.Second)
	})
	mux.HandleFunc(typeICloudImport, func(ctx context.Context, task *asynq.Task) error {
		payload, err := decodeICloudImportTask(task)
		if err != nil {
			return err
		}
		if err := service.ProcessICloudImport(ctx, payload); err != nil {
			if errors.Is(err, ErrICloudImportInvalid) || errors.Is(err, ErrICloudImportClaim) || errors.Is(err, ErrICloudImportNotFound) {
				return fmt.Errorf("discard iCloud import task: %w", asynq.SkipRetry)
			}
			if !platform.BackgroundTaskHasRetryHeadroom(ctx) {
				releaseCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
				defer cancel()
				_ = service.ReleaseICloudImport(releaseCtx, payload, "Import infrastructure is temporarily unavailable; dispatcher will retry.")
				_ = service.ScheduleICloudImportDispatcher(releaseCtx, time.Second)
				return nil
			}
			return err
		}
		_ = service.ScheduleICloudValidationDispatcher(context.WithoutCancel(ctx), 0)
		_ = service.ScheduleICloudImportDispatcher(context.WithoutCancel(ctx), 0)
		return nil
	})
	mux.HandleFunc(typeICloudValidation, func(ctx context.Context, task *asynq.Task) error {
		payload, err := decodeICloudValidationTask(task)
		if err != nil {
			return err
		}
		queueName, _ := asynq.GetQueueName(ctx)
		return handleICloudValidationTask(ctx, service, payload, queueName)
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	seed := func() {
		callCtx, callCancel := context.WithTimeout(ctx, iCloudDispatcherTaskTimeout)
		defer callCancel()
		_ = service.ScheduleICloudImportDispatcher(callCtx, 0)
		_ = service.ScheduleICloudValidationDispatcher(callCtx, 0)
		_ = service.ScheduleICloudProvisionDispatcher(callCtx, 0)
		_ = service.ScheduleICloudOnboardingDispatcher(callCtx, 0)
	}
	go func() {
		defer close(done)
		seed()
		ticker := time.NewTicker(iCloudDispatcherPeriod)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				seed()
			case <-ctx.Done():
				return
			}
		}
	}()
	var once sync.Once
	return func(shutdownCtx context.Context) {
		once.Do(cancel)
		select {
		case <-done:
		case <-shutdownCtx.Done():
		}
	}
}

func handleICloudValidationDispatcher(ctx context.Context, service *Service, sourceQueue string) error {
	if sourceQueue != "" && sourceQueue != platform.QueueBackgroundICloudValidation {
		return service.ScheduleICloudValidationDispatcher(context.WithoutCancel(ctx), 0)
	}
	_ = service.DispatchICloudValidations(ctx, iCloudValidationBatchLimit)
	return nil
}

func handleICloudValidationTask(ctx context.Context, service *Service, payload iCloudValidationTask, sourceQueue string) error {
	if sourceQueue != "" && sourceQueue != platform.QueueBackgroundICloudValidation {
		_, err := service.enqueueICloudValidation(context.WithoutCancel(ctx), payload)
		return err
	}
	release, admitted := platform.AcquireBackgroundExecution(ctx, service.backgroundExecution)
	if !admitted {
		if platform.BackgroundTaskHasRetryHeadroom(ctx) {
			return platform.ErrBackgroundExecutionDeferred
		}
		releaseICloudValidationTask(ctx, service, payload)
		return nil
	}
	defer release()
	if err := service.ProcessICloudValidation(ctx, payload); err != nil {
		if platform.BackgroundTaskHasRetryHeadroom(ctx) {
			return err
		}
		releaseICloudValidationTask(ctx, service, payload)
		return nil
	}
	_ = service.ScheduleICloudValidationDispatcher(context.WithoutCancel(ctx), time.Second)
	return nil
}

func releaseICloudValidationTask(ctx context.Context, service *Service, task iCloudValidationTask) {
	if service == nil {
		return
	}
	releaseCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	_ = service.releaseICloudValidation(releaseCtx, task, "iCloud validation is temporarily unavailable; dispatcher will retry.")
	_ = service.ScheduleICloudValidationDispatcher(releaseCtx, time.Second)
}

func decodeICloudImportTask(task *asynq.Task) (iCloudImportTask, error) {
	var payload iCloudImportTask
	if task == nil || json.Unmarshal(task.Payload(), &payload) != nil || payload.ImportID == 0 || payload.Generation == 0 {
		return iCloudImportTask{}, fmt.Errorf("decode iCloud import task: %w", asynq.SkipRetry)
	}
	return payload, nil
}

func decodeICloudValidationTask(task *asynq.Task) (iCloudValidationTask, error) {
	var payload iCloudValidationTask
	if task == nil || json.Unmarshal(task.Payload(), &payload) != nil || payload.ResourceID == 0 || payload.OwnerUserID == 0 || payload.ValidationGeneration == 0 || payload.ExpectedCredentialRevision == 0 {
		return iCloudValidationTask{}, fmt.Errorf("decode iCloud validation task: %w", asynq.SkipRetry)
	}
	return payload, nil
}

func decodeICloudOnboardingTask(task *asynq.Task) (iCloudOnboardingTask, error) {
	var payload iCloudOnboardingTask
	if task == nil || json.Unmarshal(task.Payload(), &payload) != nil || payload.TaskID == 0 || payload.Generation == 0 {
		return iCloudOnboardingTask{}, fmt.Errorf("decode iCloud onboarding task: %w", asynq.SkipRetry)
	}
	return payload, nil
}
