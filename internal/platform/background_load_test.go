package platform

import (
	"context"
	"errors"
	"math"
	"sync"
	"testing"
	"time"

	"github.com/hibiken/asynq"
	"github.com/stretchr/testify/require"
)

type systemLoadReaderStub struct {
	mu        sync.Mutex
	cpu       float64
	memory    float64
	cpuErr    error
	memoryErr error
	calls     int
}

func (s *systemLoadReaderStub) CPUPercent(context.Context) (float64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	return s.cpu, s.cpuErr
}

func (s *systemLoadReaderStub) MemoryPercent(context.Context) (float64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.memory, s.memoryErr
}

func (s *systemLoadReaderStub) set(cpuPercent, memoryPercent float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cpu = cpuPercent
	s.memory = memoryPercent
	s.cpuErr = nil
	s.memoryErr = nil
}

func (s *systemLoadReaderStub) setErrors(cpuErr, memoryErr error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cpuErr = cpuErr
	s.memoryErr = memoryErr
}

func (s *systemLoadReaderStub) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

func newBackgroundLoadControllerTestHarness(cpuPercent, memoryPercent float64) (*BackgroundLoadController, *systemLoadReaderStub) {
	load := &systemLoadReaderStub{cpu: cpuPercent, memory: memoryPercent}
	return newBackgroundLoadController(load, 128, defaultBackgroundOverloadPercent), load
}

func saturateBackgroundWindow(t *testing.T, controller *BackgroundLoadController) []func() {
	t.Helper()
	var releases []func()
	for {
		release, admitted := controller.TryAcquire()
		if !admitted {
			return releases
		}
		releases = append(releases, release)
	}
}

func releaseBackgroundPermits(releases []func()) {
	for _, release := range releases {
		release()
	}
}

func TestBackgroundLoadControllerSlowStartsThenAdditivelyRampsToMaximum(t *testing.T) {
	controller, _ := newBackgroundLoadControllerTestHarness(25, 30)
	require.Equal(t, backgroundWorkerInitial, controller.Snapshot().Limit)
	var releases []func()
	defer func() { releaseBackgroundPermits(releases) }()

	releases = append(releases, saturateBackgroundWindow(t, controller)...)
	controller.sampleAndTune(context.Background())
	require.Equal(t, backgroundWorkerInitial, controller.Snapshot().Limit)
	controller.sampleAndTune(context.Background())
	require.Equal(t, 32, controller.Snapshot().Limit)
	releases = append(releases, saturateBackgroundWindow(t, controller)...)
	controller.sampleAndTune(context.Background())
	require.Equal(t, 64, controller.Snapshot().Limit)
	releases = append(releases, saturateBackgroundWindow(t, controller)...)
	controller.sampleAndTune(context.Background())
	require.Equal(t, 64+backgroundWorkerMinimumIncreaseStep, controller.Snapshot().Limit)

	for range 32 {
		releases = append(releases, saturateBackgroundWindow(t, controller)...)
		controller.sampleAndTune(context.Background())
	}
	snapshot := controller.Snapshot()
	require.Equal(t, 128, snapshot.Limit)
	require.Equal(t, 128, snapshot.Maximum)
	require.True(t, snapshot.CPUValid)
	require.True(t, snapshot.MemoryValid)
}

func TestBackgroundLoadControllerScalesRecoveryStepForFiveHundredTwelveCeiling(t *testing.T) {
	load := &systemLoadReaderStub{cpu: 20, memory: 20}
	controller := newBackgroundLoadController(load, 512, defaultBackgroundOverloadPercent)
	require.Equal(t, 32, controller.increaseStep)
	require.Equal(t, 256, controller.slowStartThreshold)
	var releases []func()
	defer func() { releaseBackgroundPermits(releases) }()

	releases = append(releases, saturateBackgroundWindow(t, controller)...)
	controller.sampleAndTune(context.Background()) // confirm headroom
	for _, want := range []int{32, 64, 128, 256, 288} {
		controller.sampleAndTune(context.Background())
		require.Equal(t, want, controller.Snapshot().Limit)
		releases = append(releases, saturateBackgroundWindow(t, controller)...)
	}
	for range 16 {
		controller.sampleAndTune(context.Background())
		releases = append(releases, saturateBackgroundWindow(t, controller)...)
	}
	require.Equal(t, 512, controller.Snapshot().Limit)
}

func TestBackgroundLoadControllerDoesNotRampWhileIdle(t *testing.T) {
	load := &systemLoadReaderStub{cpu: 10, memory: 10}
	controller := newBackgroundLoadController(load, 512, defaultBackgroundOverloadPercent)

	for range 32 {
		controller.sampleAndTune(context.Background())
	}

	require.Equal(t, backgroundWorkerInitial, controller.Snapshot().Limit)
}

func TestBackgroundLoadControllerCapsAdditiveRecoveryAtLowWindow(t *testing.T) {
	load := &systemLoadReaderStub{cpu: 20, memory: 20}
	controller := newBackgroundLoadController(load, 512, defaultBackgroundOverloadPercent)
	controller.gate.Resize(backgroundWorkerMinimum)
	controller.slowStartThreshold = backgroundWorkerMinimum
	releases := saturateBackgroundWindow(t, controller)
	defer releaseBackgroundPermits(releases)

	controller.sampleAndTune(context.Background())
	controller.sampleAndTune(context.Background())

	require.Equal(t, 12, controller.Snapshot().Limit, "an eight-worker window must not jump directly to forty")
}

func TestBackgroundLoadControllerDoesNotSlowStartPastMeasuredCongestionPoint(t *testing.T) {
	controller, load := newBackgroundLoadControllerTestHarness(50, 20)
	controller.gate.Resize(128)
	controller.sampleAndTune(context.Background())
	require.Equal(t, 64, controller.Snapshot().Limit)

	load.set(20, 20)
	releases := saturateBackgroundWindow(t, controller)
	defer releaseBackgroundPermits(releases)
	controller.sampleAndTune(context.Background())
	require.Equal(t, 64, controller.Snapshot().Limit)
	controller.sampleAndTune(context.Background())
	require.Equal(t, 64+backgroundWorkerMinimumIncreaseStep, controller.Snapshot().Limit, "recovery must be additive above the measured safe point")
}

func TestBackgroundLoadControllerMultiplicativelyBacksOffAtCPULine(t *testing.T) {
	controller, _ := newBackgroundLoadControllerTestHarness(50, 20)
	controller.gate.Resize(128)

	want := []int{64, 32, 16, backgroundWorkerMinimum, backgroundWorkerMinimum}
	for _, limit := range want {
		controller.sampleAndTune(context.Background())
		require.Equal(t, limit, controller.Snapshot().Limit)
	}
}

func TestBackgroundLoadControllerMultiplicativelyBacksOffAtMemoryLine(t *testing.T) {
	controller, _ := newBackgroundLoadControllerTestHarness(20, 50)
	controller.gate.Resize(96)

	controller.sampleAndTune(context.Background())

	require.Equal(t, 48, controller.Snapshot().Limit)
}

func TestBackgroundLoadControllerUsesFortyToFiftyPercentHysteresisBand(t *testing.T) {
	controller, load := newBackgroundLoadControllerTestHarness(45, 35)
	controller.gate.Resize(64)

	controller.sampleAndTune(context.Background())
	require.Equal(t, 64, controller.Snapshot().Limit, "the hysteresis band must hold the current P-state")

	load.set(39.9, 39.9)
	releases := saturateBackgroundWindow(t, controller)
	defer releaseBackgroundPermits(releases)
	controller.sampleAndTune(context.Background())
	require.Equal(t, 64, controller.Snapshot().Limit, "headroom must be confirmed before ramping")
	controller.sampleAndTune(context.Background())
	require.Equal(t, 64+backgroundWorkerMinimumIncreaseStep, controller.Snapshot().Limit)

	load.set(49.9, 49.9)
	controller.sampleAndTune(context.Background())
	require.Equal(t, 64+backgroundWorkerMinimumIncreaseStep, controller.Snapshot().Limit)

	load.set(50, 30)
	controller.sampleAndTune(context.Background())
	require.Equal(t, 36, controller.Snapshot().Limit)
}

func TestBackgroundLoadControllerUsesConfiguredOverloadPercent(t *testing.T) {
	load := &systemLoadReaderStub{cpu: 50, memory: 20}
	controller := newBackgroundLoadController(load, 128, 70)
	controller.gate.Resize(128)

	controller.sampleAndTune(context.Background())
	require.Equal(t, 128, controller.Snapshot().Limit, "50 percent must not trigger the configured 70 percent overload line")

	load.set(70, 20)
	controller.sampleAndTune(context.Background())
	require.Equal(t, 64, controller.Snapshot().Limit)

	load.set(59.9, 59.9)
	releases := saturateBackgroundWindow(t, controller)
	defer releaseBackgroundPermits(releases)
	controller.sampleAndTune(context.Background())
	controller.sampleAndTune(context.Background())
	require.Equal(t, 64+backgroundWorkerMinimumIncreaseStep, controller.Snapshot().Limit)
}

func TestBackgroundLoadControllerHoldsWhenMetricsCannotProveHeadroom(t *testing.T) {
	controller, load := newBackgroundLoadControllerTestHarness(20, 20)
	controller.gate.Resize(64)
	load.setErrors(errors.New("cpu unavailable"), errors.New("memory unavailable"))

	controller.sampleAndTune(context.Background())

	snapshot := controller.Snapshot()
	require.Equal(t, 64, snapshot.Limit)
	require.False(t, snapshot.CPUValid)
	require.False(t, snapshot.MemoryValid)

	// One valid overload signal is sufficient to protect the host even if the
	// other metric is temporarily unavailable.
	load.setErrors(errors.New("cpu unavailable"), nil)
	load.mu.Lock()
	load.memory = 75
	load.mu.Unlock()
	controller.sampleAndTune(context.Background())
	require.Equal(t, 32, controller.Snapshot().Limit)
}

func TestBackgroundLoadControllerDecaysAfterConsecutiveMetricFailures(t *testing.T) {
	controller, load := newBackgroundLoadControllerTestHarness(20, 20)
	controller.gate.Resize(128)
	load.setErrors(errors.New("cpu unavailable"), errors.New("memory unavailable"))

	controller.sampleAndTune(context.Background())
	controller.sampleAndTune(context.Background())
	require.Equal(t, 128, controller.Snapshot().Limit, "brief telemetry gaps must not cause a sudden throttle")
	controller.sampleAndTune(context.Background())
	require.Equal(t, 64, controller.Snapshot().Limit, "stale telemetry must fail toward a conservative window")
	controller.sampleAndTune(context.Background())
	require.Equal(t, 32, controller.Snapshot().Limit)
}

func TestBackgroundLoadControllerStartSamplesAndStopsFeedbackLoop(t *testing.T) {
	controller, load := newBackgroundLoadControllerTestHarness(20, 20)
	controller.sampleInterval = 2 * time.Millisecond

	cleanup := controller.Start(context.Background())
	require.Eventually(t, func() bool { return load.callCount() >= 2 }, time.Second, time.Millisecond)
	stopCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	cleanup(stopCtx)
	stoppedAt := load.callCount()
	time.Sleep(10 * time.Millisecond)
	require.Equal(t, stoppedAt, load.callCount())
}

func TestAdaptiveConcurrencyGateDeniesWithoutBlockingAtLimit(t *testing.T) {
	gate := newAdaptiveConcurrencyGate(2, 4)
	releaseFirst, admitted := gate.TryAcquire()
	require.True(t, admitted)
	releaseSecond, admitted := gate.TryAcquire()
	require.True(t, admitted)

	releaseDenied, admitted := gate.TryAcquire()
	require.False(t, admitted)
	releaseDenied()
	limit, active, maximum := gate.Stats()
	require.Equal(t, 2, limit)
	require.Equal(t, 2, active)
	require.Equal(t, 4, maximum)

	releaseFirst()
	releaseFirst() // release closures are idempotent
	releaseThird, admitted := gate.TryAcquire()
	require.True(t, admitted)
	releaseSecond()
	releaseThird()
	_, active, _ = gate.Stats()
	require.Zero(t, active)
}

func TestAdaptiveConcurrencyGateResizeDownIsGraceful(t *testing.T) {
	gate := newAdaptiveConcurrencyGate(3, 4)
	releases := make([]func(), 0, 3)
	for range 3 {
		release, admitted := gate.TryAcquire()
		require.True(t, admitted)
		releases = append(releases, release)
	}
	gate.Resize(1)
	limit, active, _ := gate.Stats()
	require.Equal(t, 1, limit)
	require.Equal(t, 3, active, "resize must not cancel work already in progress")

	for index := 0; index < 2; index++ {
		releases[index]()
		_, admitted := gate.TryAcquire()
		require.False(t, admitted)
	}
	releases[2]()
	release, admitted := gate.TryAcquire()
	require.True(t, admitted)
	release()
}

func TestAdaptiveConcurrencyGateResizeUpMakesCapacityImmediatelyAvailable(t *testing.T) {
	gate := newAdaptiveConcurrencyGate(1, 4)
	releaseFirst, admitted := gate.TryAcquire()
	require.True(t, admitted)
	_, admitted = gate.TryAcquire()
	require.False(t, admitted)

	gate.Resize(2)
	releaseSecond, admitted := gate.TryAcquire()
	require.True(t, admitted)
	releaseFirst()
	releaseSecond()
}

func TestNilBackgroundLoadControllerAdmitsWithoutLimiting(t *testing.T) {
	var controller *BackgroundLoadController
	release, admitted := controller.TryAcquire()
	require.True(t, admitted)
	release()
}

func TestValidSystemPercentRejectsInvalidSamples(t *testing.T) {
	require.True(t, validSystemPercent(0))
	require.True(t, validSystemPercent(100))
	require.False(t, validSystemPercent(-0.1))
	require.False(t, validSystemPercent(100.1))
	require.False(t, validSystemPercent(math.NaN()))
	require.False(t, validSystemPercent(math.Inf(1)))
}

func TestBackgroundExecutionDeferralDoesNotCountAsFailure(t *testing.T) {
	require.False(t, backgroundIsFailure(ErrBackgroundExecutionDeferred))
	require.True(t, backgroundIsFailure(errors.New("real failure")))
}

func TestBackgroundExecutionDeferralUsesShortRetryDelay(t *testing.T) {
	task := asynq.NewTask("test", []byte("stable-payload"))
	delay := backgroundRetryDelay(0, ErrBackgroundExecutionDeferred, task)
	require.GreaterOrEqual(t, delay, backgroundRetryDelayMinimum)
	require.LessOrEqual(t, delay, backgroundRetryDelayMinimum+backgroundRetryDelayJitter)
	require.Equal(t, delay, backgroundRetryDelay(0, ErrBackgroundExecutionDeferred, task), "jitter must be stable for one durable task")
}

func TestBackgroundTasksHaveRetryHeadroomForCapacityDeferrals(t *testing.T) {
	require.Positive(t, BackgroundTaskMaxRetry)
	require.False(t, backgroundTaskHasRetryHeadroom(0, 0), "legacy MaxRetry(0) tasks require durable release")
	require.True(t, backgroundTaskHasRetryHeadroom(0, BackgroundTaskMaxRetry))
	require.False(t, backgroundTaskHasRetryHeadroom(BackgroundTaskMaxRetry, BackgroundTaskMaxRetry))
}
