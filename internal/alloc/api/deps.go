package api

import (
	"sync"

	allocapp "github.com/donnel666/remail/internal/alloc/app"
	allocinfra "github.com/donnel666/remail/internal/alloc/infra"
	"github.com/donnel666/remail/internal/platform"
	"github.com/hibiken/asynq"
	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
)

type Module struct {
	UseCase                   *allocapp.UseCase
	Repo                      *allocinfra.Repo
	ResourceGuard             *ResourceAllocationGuardAdapter
	BackgroundExecution       BackgroundExecutionGate
	InventoryRefreshExecution BackgroundExecutionGate
}

type BackgroundExecutionGate interface {
	TryAcquire() (release func(), admitted bool)
}

type inventoryRefreshGate struct {
	mu     sync.Mutex
	limit  int
	active int
}

func newInventoryRefreshGate(limit int) *inventoryRefreshGate {
	if limit < 1 {
		limit = 1
	}
	return &inventoryRefreshGate{limit: limit}
}

func (g *inventoryRefreshGate) TryAcquire() (func(), bool) {
	if g == nil {
		return func() {}, true
	}
	g.mu.Lock()
	if g.active >= g.limit {
		g.mu.Unlock()
		return func() {}, false
	}
	g.active++
	g.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			g.mu.Lock()
			g.active--
			g.mu.Unlock()
		})
	}, true
}

func (m *Module) SetBackgroundExecutionGate(gate BackgroundExecutionGate) {
	if m != nil {
		m.BackgroundExecution = gate
	}
}

func NewModule(db *gorm.DB, redisClient redis.UniversalClient, asynqClient *asynq.Client) *Module {
	repo := allocinfra.NewRepo(db, redisClient)
	queue := allocinfra.NewInventoryRefreshQueue(asynqClient)
	useCase := allocapp.NewUseCase(repo, queue)
	if redisClient != nil {
		useCase.SetInventoryCache(allocinfra.NewInventoryCache(redisClient))
		useCase.SetPrivateInventoryCache(platform.NewHourlySnapshotCache[allocapp.PrivateInventoryTotals](redisClient))
	}
	useCase.SetAdminAllocationEnrichmentPort(allocinfra.NewAdminAllocationEnrichmentRepo(db))
	return &Module{
		UseCase:                   useCase,
		Repo:                      repo,
		ResourceGuard:             NewResourceAllocationGuardAdapter(useCase),
		InventoryRefreshExecution: newInventoryRefreshGate(allocapp.InventoryRefreshConcurrency),
	}
}
