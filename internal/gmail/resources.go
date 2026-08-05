package gmail

import (
	"context"
	"database/sql"
	"encoding/base32"
	"errors"
	"fmt"
	"hash/fnv"
	"log/slog"
	"math/rand/v2"
	stdmail "net/mail"
	"strconv"
	"strings"
	"time"
	"unicode"

	allocdomain "github.com/donnel666/remail/internal/alloc/domain"
	governancedomain "github.com/donnel666/remail/internal/governance/domain"
	"github.com/donnel666/remail/internal/money"
	"github.com/donnel666/remail/internal/platform"
	"github.com/donnel666/remail/internal/systemsettings/runtimeconfig"
	tradeapp "github.com/donnel666/remail/internal/trade/app"
	tradedomain "github.com/donnel666/remail/internal/trade/domain"
	"github.com/go-sql-driver/mysql"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	localGmailAllocationBucketCount    = 2048
	localGmailCandidateWindow          = 4
	localGmailGlobalCandidateWindow    = 8
	localGmailBucketProbeCount         = 4
	localGmailCandidateRetryCount      = 5
	localGmailCandidateRetryDelay      = 10 * time.Millisecond
	localGmailMaxCandidateWindow       = 100
	localGmailMaxBucketProbeCount      = 64
	localGmailMaxCandidateRetryCount   = 20
	localGmailDotAliasCapacity         = 10
	localGmailMaxDotAliasCapacity      = 64
	localGmailAliasGenerationWindow    = 32
	localGmailMaxAliasGenerationWindow = 1000
)

type localGmailProductConfig struct {
	ProjectID  uint
	ProductID  uint
	MainWeight int
	DotWeight  int
	PlusWeight int
}

type LocalResourceListFilter struct {
	Search string
	Status string
	Offset int
	Limit  int
}

type localAllocationGuardModel struct {
	OrderNo   string    `gorm:"column:order_no;primaryKey"`
	Type      string    `gorm:"column:type"`
	CreatedAt time.Time `gorm:"column:created_at"`
}

func (localAllocationGuardModel) TableName() string { return "allocation_order_guards" }

type localResourceImportLine struct {
	lineNumber      int
	email           string
	identity        string
	password        string
	twoFactorSecret string
	appPassword     string
}

func (s *Service) ListLocalResources(ctx context.Context, filter LocalResourceListFilter) (*LocalResourceList, error) {
	filter.Search = strings.ToLower(strings.TrimSpace(filter.Search))
	filter.Status = strings.ToLower(strings.TrimSpace(filter.Status))
	if filter.Status != "" && !isLocalResourceStatus(filter.Status) {
		return nil, ErrInvalidLocalResource
	}
	if filter.Offset < 0 {
		filter.Offset = 0
	}
	if filter.Limit <= 0 || filter.Limit > 200 {
		filter.Limit = 20
	}

	db := s.dbFor(ctx).Table("gmail_resources AS gr").
		Joins("JOIN email_resources AS er ON er.id = gr.id AND er.type = ?", "gmail")
	if filter.Status == "" {
		db = db.Where("gr.status <> ?", LocalResourceDeleted)
	}
	if filter.Search != "" {
		db = db.Where("gr.email LIKE ?", "%"+filter.Search+"%")
	}
	if filter.Status != "" {
		if filter.Status == LocalResourceNormal {
			db = db.Where("gr.status IN (?, ?, ?, ?)", LocalResourceNormal, localResourceRollbackNormal, localResourceRollbackLeased, localResourceRollbackSold)
		} else {
			db = db.Where("gr.status = ?", filter.Status)
		}
	}
	var total int64
	if err := db.Count(&total).Error; err != nil {
		return nil, fmt.Errorf("count local Gmail resources: %w", err)
	}
	var rows []struct {
		ID                    uint       `gorm:"column:id"`
		Version               uint64     `gorm:"column:version"`
		OwnerUserID           uint       `gorm:"column:owner_user_id"`
		Email                 string     `gorm:"column:email"`
		Status                string     `gorm:"column:status"`
		ForSale               bool       `gorm:"column:for_sale"`
		PasswordConfigured    bool       `gorm:"column:password_configured"`
		TwoFactorConfigured   bool       `gorm:"column:two_factor_configured"`
		AppPasswordConfigured bool       `gorm:"column:app_password_configured"`
		CredentialRevision    uint64     `gorm:"column:credential_revision"`
		ValidationFailures    int        `gorm:"column:validation_failures"`
		LastAllocatedAt       *time.Time `gorm:"column:last_allocated_at"`
		LastSafeError         string     `gorm:"column:last_safe_error"`
		LastCheckedAt         *time.Time `gorm:"column:last_checked_at"`
		CreatedAt             time.Time  `gorm:"column:created_at"`
		UpdatedAt             time.Time  `gorm:"column:updated_at"`
	}
	if err := db.Select(`gr.id, er.version, er.owner_user_id, gr.email, gr.status, gr.for_sale,
gr.password <> '' AS password_configured,
gr.two_factor_secret <> '' AS two_factor_configured, gr.app_password <> '' AS app_password_configured,
gr.credential_revision, gr.validation_failures, gr.last_allocated_at,
gr.last_safe_error, gr.last_checked_at, gr.created_at, gr.updated_at`).
		Order("gr.id DESC").Offset(filter.Offset).Limit(filter.Limit).Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("list local Gmail resources: %w", err)
	}

	items := make([]LocalResourceItem, len(rows))
	for i := range rows {
		status := rows[i].Status
		if status == localResourceRollbackNormal || status == localResourceRollbackLeased || status == localResourceRollbackSold {
			status = LocalResourceNormal
		}
		items[i] = LocalResourceItem{
			ID: rows[i].ID, Version: rows[i].Version, OwnerUserID: rows[i].OwnerUserID,
			Email: rows[i].Email, Status: status, ForSale: rows[i].ForSale,
			PasswordConfigured: rows[i].PasswordConfigured, TwoFactorConfigured: rows[i].TwoFactorConfigured,
			AppPasswordConfigured: rows[i].AppPasswordConfigured,
			CredentialRevision:    rows[i].CredentialRevision, ValidationFailures: rows[i].ValidationFailures,
			LastAllocatedAt: rows[i].LastAllocatedAt, LastSafeError: rows[i].LastSafeError,
			LastCheckedAt: rows[i].LastCheckedAt, CreatedAt: rows[i].CreatedAt, UpdatedAt: rows[i].UpdatedAt,
		}
	}
	facets, err := s.localResourceFacets(ctx, filter.Search)
	if err != nil {
		return nil, err
	}
	return &LocalResourceList{Items: items, Total: total, Offset: filter.Offset, Limit: filter.Limit, Facets: facets}, nil
}

func (s *Service) localResourceFacets(ctx context.Context, search string) (LocalResourceFacets, error) {
	type row struct {
		Status string `gorm:"column:status"`
		Count  int64  `gorm:"column:count"`
	}
	db := s.dbFor(ctx).Model(&localResourceModel{}).Select("status, COUNT(*) AS count")
	if search != "" {
		db = db.Where("email LIKE ?", "%"+search+"%")
	}
	var rows []row
	if err := db.Group("status").Scan(&rows).Error; err != nil {
		return LocalResourceFacets{}, fmt.Errorf("count local Gmail resource facets: %w", err)
	}
	var facets LocalResourceFacets
	for _, item := range rows {
		if item.Status == LocalResourceDeleted {
			continue
		}
		facets.All += item.Count
		switch item.Status {
		case LocalResourcePending:
			facets.Pending = item.Count
		case LocalResourceValidating:
			facets.Validating = item.Count
		case LocalResourceIdentifying:
			facets.Identifying = item.Count
		case LocalResourceNormal, localResourceRollbackNormal, localResourceRollbackLeased, localResourceRollbackSold:
			facets.Normal += item.Count
		case LocalResourceAbnormal:
			facets.Abnormal = item.Count
		case LocalResourceDisabled:
			facets.Disabled = item.Count
		}
	}
	return facets, nil
}

func parseLocalResourceImportLine(raw string) (localResourceImportLine, bool) {
	parts := strings.Split(raw, "----")
	if len(parts) != 4 {
		return localResourceImportLine{}, false
	}
	email := strings.ToLower(strings.TrimSpace(parts[0]))
	password := parts[1]
	twoFactorSecret := strings.ToUpper(strings.TrimRight(removeWhitespace(parts[2]), "="))
	appPassword := strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return -1
		}
		return r
	}, parts[3])
	if len(email) > 320 || strings.TrimSpace(password) == "" || len(password) > 512 ||
		twoFactorSecret == "" || len(twoFactorSecret) > 512 || appPassword == "" || len(appPassword) > 128 {
		return localResourceImportLine{}, false
	}
	address, err := stdmail.ParseAddress(email)
	if err != nil || !strings.EqualFold(strings.TrimSpace(address.Address), email) {
		return localResourceImportLine{}, false
	}
	local, domain, ok := strings.Cut(email, "@")
	if !ok || domain != "gmail.com" && domain != "googlemail.com" {
		return localResourceImportLine{}, false
	}
	if strings.Contains(local, "+") {
		return localResourceImportLine{}, false
	}
	canonicalLocal := strings.ReplaceAll(local, ".", "")
	if canonicalLocal == "" {
		return localResourceImportLine{}, false
	}
	if _, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(twoFactorSecret); err != nil {
		return localResourceImportLine{}, false
	}
	return localResourceImportLine{
		email: email, identity: canonicalLocal + "@gmail.com", password: password,
		twoFactorSecret: twoFactorSecret, appPassword: appPassword,
	}, true
}

func removeWhitespace(value string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return -1
		}
		return r
	}, value)
}

func (s *Service) AllocateLocalCode(
	ctx context.Context,
	orderNo string,
	buyerUserID, projectID, productID uint,
	policy tradedomain.SupplyPolicy,
	quote tradeapp.GmailSupplyQuote,
) (*tradeapp.GmailLocalAllocation, error) {
	orderNo = strings.TrimSpace(orderNo)
	cost, err := money.Parse(quote.CostPoints)
	if orderNo == "" || buyerUserID == 0 || projectID == 0 || productID == 0 ||
		strings.TrimSpace(quote.Source) != SourceLocal || err != nil || cost.IsNegative() {
		return nil, ErrInvalidRoute
	}
	allocation, _, err := s.allocateLocalResource(
		ctx, orderNo, buyerUserID, projectID, productID, tradedomain.ServiceModeCode, policy, cost,
	)
	if err != nil {
		return nil, err
	}
	return &tradeapp.GmailLocalAllocation{
		AllocationID: allocation.ID,
		Email:        allocation.Email,
		SupplyScope:  tradeapp.SupplyScope(allocation.SupplyScope),
	}, nil
}

func (s *Service) AllocateLocalPurchase(
	ctx context.Context,
	orderNo string,
	buyerUserID, projectID, productID uint,
	policy tradedomain.SupplyPolicy,
	quote tradeapp.GmailSupplyQuote,
) (*tradeapp.GmailPurchaseDelivery, error) {
	orderNo = strings.TrimSpace(orderNo)
	cost, err := money.Parse(quote.CostPoints)
	if orderNo == "" || buyerUserID == 0 || projectID == 0 || productID == 0 ||
		strings.TrimSpace(quote.Source) != SourceLocal || err != nil || cost.IsNegative() {
		return nil, ErrInvalidRoute
	}
	allocation, resource, err := s.allocateLocalResource(
		ctx, orderNo, buyerUserID, projectID, productID, tradedomain.ServiceModePurchase, policy, cost,
	)
	if err != nil {
		return nil, err
	}
	return &tradeapp.GmailPurchaseDelivery{
		AllocationID: allocation.ID, ResourceID: resource.ID,
		SupplyScope: tradeapp.SupplyScope(allocation.SupplyScope), Email: allocation.Email, Password: resource.Password,
		TwoFactorSecret: resource.TwoFactorSecret, AppPassword: resource.AppPassword,
	}, nil
}

func (s *Service) allocateLocalResource(
	ctx context.Context,
	orderNo string,
	buyerUserID, projectID, productID uint,
	mode tradedomain.ServiceMode,
	policy tradedomain.SupplyPolicy,
	cost decimal.Decimal,
) (allocationResult *allocationModel, resourceResult *localResourceModel, runErr error) {
	startedAt := time.Now()
	existingHit := false
	defer func() {
		metricResult := localGmailAllocationMetricResult(runErr, existingHit)
		platform.ObserveAllocationDuration("gmail", metricResult, startedAt)
		platform.RecordAllocationResult("gmail", metricResult)
	}()

	orderNo = strings.TrimSpace(orderNo)
	if orderNo == "" || buyerUserID == 0 || projectID == 0 || productID == 0 ||
		(mode != tradedomain.ServiceModeCode && mode != tradedomain.ServiceModePurchase) ||
		(policy != tradedomain.SupplyPolicyPrivateFirst && policy != tradedomain.SupplyPolicyPublicOnly) || cost.IsNegative() {
		return nil, nil, allocdomain.ErrInvalidAllocationRequest
	}
	scopes := []string{AllocationSupplyPublic}
	if policy == tradedomain.SupplyPolicyPrivateFirst {
		scopes = []string{AllocationSupplyOwned, AllocationSupplyPublic}
	}

	attempts := localGmailCandidateRetryCountValue()
	if _, parentTx := platform.GormTxFromContext(ctx); parentTx {
		attempts = 1
	}
	var err error
	for attempt := 0; attempt < attempts; attempt++ {
		allocationResult, resourceResult = nil, nil
		existingHit = false
		err = s.withLocalGmailAllocationTx(ctx, func(txCtx context.Context) error {
			tx := s.dbFor(txCtx)
			allocation, resource, existing, err := s.allocateLocalGmailOnce(
				tx, orderNo, buyerUserID, projectID, productID, mode, scopes, cost,
			)
			if err != nil {
				return err
			}
			allocationResult, resourceResult, existingHit = allocation, resource, existing
			return nil
		})
		if err == nil || (!errors.Is(err, tradedomain.ErrInsufficientInventory) && !errors.Is(err, allocdomain.ErrAllocationConflict)) {
			break
		}
		if attempt < attempts-1 {
			select {
			case <-ctx.Done():
				return nil, nil, ctx.Err()
			case <-time.After(localGmailCandidateRetryDelay):
			}
		}
	}
	if err != nil {
		return nil, nil, err
	}
	if allocationResult == nil || resourceResult == nil {
		return nil, nil, tradedomain.ErrInsufficientInventory
	}
	return allocationResult, resourceResult, nil
}

func (s *Service) allocateLocalGmailOnce(
	tx *gorm.DB,
	orderNo string,
	buyerUserID, projectID, productID uint,
	mode tradedomain.ServiceMode,
	scopes []string,
	cost decimal.Decimal,
) (*allocationModel, *localResourceModel, bool, error) {
	var allocation allocationModel
	var resource localResourceModel
	err := tx.Where("order_no = ?", orderNo).Take(&allocation).Error
	if err == nil {
		if allocation.GuardType != "gmail" || allocation.Source != SourceLocal || allocation.ServiceMode != string(mode) ||
			allocation.ProjectID != projectID || allocation.ProductID != productID || allocation.ResourceID == nil ||
			allocation.Status != AllocationStatusAllocated || !isGmailMailbox(allocation.Mailbox) ||
			(allocation.SupplyScope != AllocationSupplyOwned && allocation.SupplyScope != AllocationSupplyPublic) {
			return nil, nil, false, allocdomain.ErrAllocationConflict
		}
		var guard localAllocationGuardModel
		if err := tx.Where("order_no = ? AND type = ?", orderNo, "gmail").Take(&guard).Error; err != nil {
			return nil, nil, false, allocdomain.ErrAllocationConflict
		}
		if err := tx.Where("id = ?", *allocation.ResourceID).Take(&resource).Error; err != nil {
			return nil, nil, false, fmt.Errorf("load allocated local Gmail resource: %w", err)
		}
		return &allocation, &resource, true, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil, false, fmt.Errorf("find local Gmail allocation: %w", err)
	}
	config, err := loadLocalGmailAllocationProduct(tx, projectID, productID, buyerUserID, mode)
	if err != nil {
		return nil, nil, false, err
	}
	preferences := gmailMailboxPreferences(orderNo, *config)
	if len(preferences) == 0 {
		return nil, nil, false, allocdomain.ErrProjectNotAllocatable
	}

	lockedRoots := make(map[uint]struct{})
	for _, scope := range scopes {
		for _, mailbox := range preferences {
			buckets := localGmailBucketProbeSequence(orderNo, projectID, mailbox)
			for _, bucket := range buckets {
				locked, err := s.tryLocalGmailCandidateWindow(
					tx, orderNo, buyerUserID, projectID, productID, mode, scope, mailbox, cost,
					&bucket, localGmailCandidateWindowValue(), lockedRoots,
				)
				if err != nil {
					return nil, nil, false, err
				}
				if locked != nil {
					return locked.allocation, locked.resource, false, nil
				}
			}
			platform.RecordAllocationBucketFallback("gmail", "probes_exhausted")
			locked, err := s.tryLocalGmailCandidateWindow(
				tx, orderNo, buyerUserID, projectID, productID, mode, scope, mailbox, cost,
				nil, localGmailGlobalCandidateWindowValue(), lockedRoots,
			)
			if err != nil {
				return nil, nil, false, err
			}
			if locked != nil {
				return locked.allocation, locked.resource, false, nil
			}
		}
	}
	return nil, nil, false, tradedomain.ErrInsufficientInventory
}

type localGmailAllocationResult struct {
	allocation *allocationModel
	resource   *localResourceModel
}

func (s *Service) tryLocalGmailCandidateWindow(
	tx *gorm.DB,
	orderNo string,
	buyerUserID, projectID, productID uint,
	mode tradedomain.ServiceMode,
	scope string,
	mailbox string,
	cost decimal.Decimal,
	bucket *uint16,
	limit int,
	lockedRoots map[uint]struct{},
) (*localGmailAllocationResult, error) {
	candidates, err := listLocalGmailAllocationCandidates(tx, projectID, buyerUserID, scope, mailbox, bucket, limit)
	if err != nil {
		return nil, err
	}
	for _, candidateID := range candidates {
		platform.AddAllocationCandidateAttempts("gmail", 1)
		if _, alreadyLocked := lockedRoots[candidateID]; !alreadyLocked {
			rootLocked, err := lockLocalGmailAllocationRoot(tx, candidateID, len(lockedRoots) > 0)
			if err != nil {
				return nil, err
			}
			if !rootLocked {
				platform.RecordAllocationResourceLockSkip("gmail")
				continue
			}
			lockedRoots[candidateID] = struct{}{}
		}
		resource, err := lockLocalGmailAllocationCandidate(tx, candidateID, projectID, buyerUserID, scope, mailbox)
		if err != nil {
			return nil, err
		}
		if resource == nil {
			platform.RecordAllocationCandidateRecheckMiss("gmail")
			continue
		}
		email, available, err := selectLocalGmailMailbox(tx, *resource, projectID, orderNo, mailbox)
		if err != nil {
			return nil, err
		}
		if !available {
			continue
		}
		createdAt := s.now().UTC()
		guard := localAllocationGuardModel{OrderNo: orderNo, Type: "gmail", CreatedAt: createdAt}
		guardResult := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&guard)
		if guardResult.Error != nil {
			return nil, fmt.Errorf("create local Gmail allocation guard: %w", guardResult.Error)
		}
		if guardResult.RowsAffected != 1 {
			return nil, allocdomain.ErrAllocationConflict
		}
		resourceID := resource.ID
		allocationCost := cost
		if scope == AllocationSupplyOwned {
			allocationCost = decimal.Zero
		}
		allocation := &allocationModel{
			OrderNo: orderNo, GuardType: "gmail", ProjectID: projectID, ProductID: productID,
			Source: SourceLocal, ServiceMode: string(mode), ResourceID: &resourceID,
			SupplyScope: scope, Mailbox: mailbox, Email: email, Status: AllocationStatusAllocated,
			CostPointsSnapshot: money.Format(allocationCost), CreatedAt: createdAt,
		}
		if err := tx.Create(allocation).Error; err != nil {
			if isLocalGmailDuplicateKey(err) {
				return nil, allocdomain.ErrAllocationConflict
			}
			return nil, fmt.Errorf("create local Gmail allocation: %w", err)
		}
		if err := tx.Model(&localResourceModel{}).Where("id = ?", resource.ID).
			Update("last_allocated_at", createdAt).Error; err != nil {
			return nil, fmt.Errorf("touch local Gmail allocation: %w", err)
		}
		resource.LastAllocatedAt = &createdAt
		return &localGmailAllocationResult{allocation: allocation, resource: resource}, nil
	}
	return nil, nil
}

func loadLocalGmailAllocationProduct(
	tx *gorm.DB,
	projectID, productID, buyerUserID uint,
	mode tradedomain.ServiceMode,
) (*localGmailProductConfig, error) {
	modeColumn := "pp.code_enabled"
	if mode == tradedomain.ServiceModePurchase {
		modeColumn = "pp.purchase_enabled"
	}
	var config localGmailProductConfig
	result := tx.Table("project_products AS pp").
		Select("pp.project_id, pp.id AS product_id, pp.main_weight, pp.dot_weight, pp.plus_weight").
		Joins("JOIN projects AS p ON p.id = pp.project_id").
		Where("pp.id = ? AND pp.project_id = ? AND pp.type = ? AND pp.status = ? AND "+modeColumn+" = ?",
			productID, projectID, "gmail", "enabled", true).
		Where("p.status = ?", "listed").
		Where("p.access_type = ? OR EXISTS (SELECT 1 FROM project_accesses AS pa WHERE pa.project_id = p.id AND pa.user_id = ?)", "public", buyerUserID).
		Limit(1).Scan(&config)
	if result.Error != nil {
		return nil, fmt.Errorf("load local Gmail allocation product: %w", result.Error)
	}
	if config.ProductID == 0 || config.MainWeight+config.DotWeight+config.PlusWeight <= 0 {
		return nil, allocdomain.ErrProjectNotAllocatable
	}
	return &config, nil
}

func listLocalGmailAllocationCandidates(
	tx *gorm.DB,
	projectID, buyerUserID uint,
	scope string,
	mailbox string,
	bucket *uint16,
	limit int,
) ([]uint, error) {
	query := tx.Table("gmail_resources AS r").
		Select("r.id").
		Joins("JOIN email_resources AS er ON er.id = r.id AND er.type = ?", "gmail").
		Joins("JOIN users AS owner ON owner.id = er.owner_user_id").
		Where("r.status IN (?, ?)", LocalResourceNormal, localResourceRollbackNormal)
	if mailbox == GmailMailboxMain {
		query = query.
			Where("NOT EXISTS (SELECT 1 FROM gmail_allocations AS active WHERE active.resource_id = r.id AND active.mailbox = ? AND active.status = ?)", GmailMailboxMain, AllocationStatusAllocated).
			Where("NOT EXISTS (SELECT 1 FROM gmail_allocations AS history WHERE history.resource_id = r.id AND history.project_id = ? AND history.mailbox = ?)", projectID, GmailMailboxMain)
	}
	if scope == AllocationSupplyOwned {
		query = query.Where("r.for_sale = ? AND er.owner_user_id = ?", false, buyerUserID)
	} else {
		query = query.Where("r.for_sale = ? AND owner.status = ? AND owner.role IN ?", true, "active", []string{"supplier", "admin", "super_admin"})
	}
	if bucket != nil {
		query = query.Where("r.alloc_bucket = ?", *bucket)
	}
	var ids []uint
	if err := query.Order("r.last_allocated_at ASC, r.id ASC").Limit(limit).Pluck("r.id", &ids).Error; err != nil {
		return nil, fmt.Errorf("list local Gmail allocation candidates: %w", err)
	}
	return ids, nil
}

func lockLocalGmailAllocationRoot(tx *gorm.DB, resourceID uint, skipLocked bool) (bool, error) {
	var id uint
	if tx.Name() != "mysql" {
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Model(&resourceRootModel{}).
			Where("id = ? AND type = ?", resourceID, "gmail").Pluck("id", &id).Error
		return id != 0, err
	}
	query := "SELECT id FROM email_resources WHERE id = ? AND type = ? LIMIT 1 FOR UPDATE"
	if skipLocked {
		query += " SKIP LOCKED"
	}
	if err := tx.Raw(query, resourceID, "gmail").Scan(&id).Error; err != nil {
		return false, fmt.Errorf("lock local Gmail allocation root: %w", err)
	}
	return id != 0, nil
}

func lockLocalGmailAllocationCandidate(
	tx *gorm.DB,
	resourceID, projectID, buyerUserID uint,
	scope, mailbox string,
) (*localResourceModel, error) {
	query := tx.Table("gmail_resources AS r").
		Select("r.*").
		Joins("JOIN email_resources AS er ON er.id = r.id AND er.type = ?", "gmail").
		Joins("JOIN users AS owner ON owner.id = er.owner_user_id").
		Where("r.id = ? AND r.status IN (?, ?)", resourceID, LocalResourceNormal, localResourceRollbackNormal)
	if mailbox == GmailMailboxMain {
		query = query.
			Where("NOT EXISTS (SELECT 1 FROM gmail_allocations AS active WHERE active.resource_id = r.id AND active.mailbox = ? AND active.status = ?)", GmailMailboxMain, AllocationStatusAllocated).
			Where("NOT EXISTS (SELECT 1 FROM gmail_allocations AS history WHERE history.resource_id = r.id AND history.project_id = ? AND history.mailbox = ?)", projectID, GmailMailboxMain)
	}
	if scope == AllocationSupplyOwned {
		query = query.Where("r.for_sale = ? AND er.owner_user_id = ?", false, buyerUserID)
	} else {
		query = query.Where("r.for_sale = ? AND owner.status = ? AND owner.role IN ?", true, "active", []string{"supplier", "admin", "super_admin"})
	}
	if tx.Name() == "mysql" {
		query = query.Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"})
	} else {
		query = query.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	var resource localResourceModel
	result := query.Limit(1).Scan(&resource)
	if result.Error != nil {
		return nil, fmt.Errorf("lock local Gmail allocation candidate: %w", result.Error)
	}
	if resource.ID == 0 {
		return nil, nil
	}
	return &resource, nil
}

func selectLocalGmailMailbox(
	tx *gorm.DB,
	resource localResourceModel,
	projectID uint,
	orderNo, mailbox string,
) (string, bool, error) {
	var candidates []string
	switch mailbox {
	case GmailMailboxMain:
		candidates = []string{strings.ToLower(strings.TrimSpace(resource.Email))}
	case GmailMailboxDot:
		candidates = gmailDotAliasVariants(resource.Email)
	case GmailMailboxPlus:
		candidates = gmailPlusAliasVariants(resource.Email, projectID, orderNo)
	default:
		return "", false, allocdomain.ErrInvalidAllocationRequest
	}
	for _, email := range candidates {
		available, err := localGmailMailboxAvailable(tx, resource.ID, projectID, mailbox, email)
		if err != nil {
			return "", false, err
		}
		if available {
			return email, true, nil
		}
	}
	return "", false, nil
}

func localGmailMailboxAvailable(tx *gorm.DB, resourceID, projectID uint, mailbox, email string) (bool, error) {
	var unavailable bool
	var result *gorm.DB
	if mailbox == GmailMailboxMain {
		result = tx.Raw(`SELECT EXISTS (
    SELECT 1
    FROM gmail_allocations
    WHERE resource_id = ?
      AND mailbox = 'main'
      AND (project_id = ? OR status = 'allocated')
    LIMIT 1
)`, resourceID, projectID).Scan(&unavailable)
	} else {
		result = tx.Raw(`SELECT EXISTS (
    SELECT 1
    FROM gmail_allocations
    WHERE project_id = ? AND mailbox = ? AND email = ?
    LIMIT 1
)`, projectID, mailbox, strings.ToLower(strings.TrimSpace(email))).Scan(&unavailable)
	}
	if result.Error != nil {
		return false, fmt.Errorf("check local Gmail mailbox history: %w", result.Error)
	}
	return !unavailable, nil
}

func gmailMailboxPreferences(orderNo string, config localGmailProductConfig) []string {
	type weightedMailbox struct {
		mailbox string
		weight  int
	}
	weights := []weightedMailbox{
		{mailbox: GmailMailboxMain, weight: config.MainWeight},
		{mailbox: GmailMailboxDot, weight: config.DotWeight},
		{mailbox: GmailMailboxPlus, weight: config.PlusWeight},
	}
	total := 0
	for _, item := range weights {
		if item.weight > 0 {
			total += item.weight
		}
	}
	if total <= 0 {
		return nil
	}
	pick := int(localGmailHash64(orderNo+"|"+strconv.FormatUint(uint64(config.ProductID), 10)) % uint64(total))
	selected := GmailMailboxMain
	running := 0
	for _, item := range weights {
		if item.weight <= 0 {
			continue
		}
		running += item.weight
		if pick < running {
			selected = item.mailbox
			break
		}
	}
	result := []string{selected}
	for _, item := range weights {
		if item.weight > 0 && item.mailbox != selected {
			result = append(result, item.mailbox)
		}
	}
	return result
}

func gmailDotAliasVariants(email string) []string {
	local, domainPart, ok := splitLocalGmailAddress(email)
	if !ok || len(local) < 2 {
		return nil
	}
	limit := min(len(local)-1, localGmailDotAliasCapacityValue())
	result := make([]string, 0, limit)
	for i := 1; i <= limit; i++ {
		if local[i-1] == '.' || local[i] == '.' {
			continue
		}
		alias := local[:i] + "." + local[i:] + "@" + domainPart
		if len(alias) <= 320 {
			result = append(result, alias)
		}
	}
	return result
}

func gmailPlusAliasVariants(email string, projectID uint, orderNo string) []string {
	local, domainPart, ok := splitLocalGmailAddress(email)
	if !ok || local == "" {
		return nil
	}
	base := strconv.FormatUint(uint64(projectID), 36) + strconv.FormatUint(localGmailHash64(orderNo)%46656, 36)
	window := localGmailAliasGenerationWindowValue()
	result := make([]string, 0, window)
	for i := 0; i < window; i++ {
		alias := local + "+p" + base + strconv.FormatInt(int64(i), 36) + "@" + domainPart
		if len(alias) <= 320 {
			result = append(result, alias)
		}
	}
	return result
}

func splitLocalGmailAddress(email string) (string, string, bool) {
	email = strings.ToLower(strings.TrimSpace(email))
	index := strings.LastIndexByte(email, '@')
	if index <= 0 || index == len(email)-1 {
		return "", "", false
	}
	return email[:index], email[index+1:], true
}

func isGmailMailbox(mailbox string) bool {
	return mailbox == GmailMailboxMain || mailbox == GmailMailboxDot || mailbox == GmailMailboxPlus
}

func (s *Service) withLocalGmailAllocationTx(ctx context.Context, fn func(context.Context) error) error {
	if _, ok := platform.GormTxFromContext(ctx); ok {
		return fn(ctx)
	}
	var err error
	for attempt := 0; attempt < 2; attempt++ {
		txOptions := &sql.TxOptions{Isolation: sql.LevelReadCommitted}
		if s.db.Name() != "mysql" {
			txOptions = nil
		}
		err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			return fn(platform.WithGormTx(ctx, tx))
		}, txOptions)
		if err == nil || !isLocalGmailDeadlock(err) {
			return err
		}
		platform.RecordMySQLTransactionEvent("gmail_alloc", localGmailMySQLRetryEvent(err))
		if !isLocalGmailWholeTransactionRollback(err) || attempt == 1 {
			if attempt == 1 {
				platform.RecordMySQLTransactionEvent("gmail_alloc", "retry_exhausted")
			}
			return err
		}
		platform.RecordMySQLTransactionEvent("gmail_alloc", "retry")
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(localGmailDeadlockBackoff(attempt)):
		}
	}
	return err
}

func localGmailCandidateWindowValue() int {
	return min(runtimeconfig.Int("candidate_window_size", localGmailCandidateWindow, 1), localGmailMaxCandidateWindow)
}

func localGmailGlobalCandidateWindowValue() int {
	return min(runtimeconfig.Int("global_candidate_window", localGmailGlobalCandidateWindow, 1), localGmailMaxCandidateWindow)
}

func localGmailBucketProbeCountValue() int {
	return min(runtimeconfig.Int("bucket_probe_count", localGmailBucketProbeCount, 1), localGmailMaxBucketProbeCount)
}

func localGmailCandidateRetryCountValue() int {
	return min(runtimeconfig.Int("candidate_retry_count", localGmailCandidateRetryCount, 1), localGmailMaxCandidateRetryCount)
}

func localGmailDotAliasCapacityValue() int {
	return min(runtimeconfig.Int("dot_alias_capacity_per_resource", localGmailDotAliasCapacity, 1), localGmailMaxDotAliasCapacity)
}

func localGmailAliasGenerationWindowValue() int {
	return min(runtimeconfig.Int("alias_generation_window", localGmailAliasGenerationWindow, 1), localGmailMaxAliasGenerationWindow)
}

func localGmailBucketProbeSequence(orderNo string, projectID uint, mailbox string) []uint16 {
	start := uint16(localGmailHash64(orderNo+"|"+strconv.FormatUint(uint64(projectID), 10)+"|gmail|"+mailbox) % localGmailAllocationBucketCount)
	count := min(localGmailBucketProbeCountValue(), localGmailAllocationBucketCount)
	buckets := make([]uint16, 0, count)
	for i := 0; i < count; i++ {
		buckets = append(buckets, uint16((int(start)+i)%localGmailAllocationBucketCount))
	}
	return buckets
}

func localGmailHash64(value string) uint64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(value))
	return h.Sum64()
}

func localGmailAllocationMetricResult(err error, existing bool) string {
	switch {
	case err == nil && existing:
		return "existing"
	case err == nil:
		return "succeeded"
	case errors.Is(err, tradedomain.ErrInsufficientInventory):
		return "insufficient_inventory"
	case errors.Is(err, allocdomain.ErrAllocationConflict):
		return "conflict"
	case errors.Is(err, allocdomain.ErrInvalidAllocationRequest), errors.Is(err, allocdomain.ErrProjectNotAllocatable):
		return "invalid_request"
	default:
		return "system_failed"
	}
}

func isLocalGmailDuplicateKey(err error) bool {
	var mysqlErr *mysql.MySQLError
	return errors.Is(err, gorm.ErrDuplicatedKey) || errors.As(err, &mysqlErr) && mysqlErr.Number == 1062
}

func isLocalGmailDeadlock(err error) bool {
	var mysqlErr *mysql.MySQLError
	return errors.As(err, &mysqlErr) && (mysqlErr.Number == 1213 || mysqlErr.Number == 1205)
}

func isLocalGmailWholeTransactionRollback(err error) bool {
	var mysqlErr *mysql.MySQLError
	return errors.As(err, &mysqlErr) && mysqlErr.Number == 1213
}

func localGmailMySQLRetryEvent(err error) string {
	var mysqlErr *mysql.MySQLError
	if errors.As(err, &mysqlErr) && mysqlErr.Number == 1205 {
		return "1205"
	}
	return "1213"
}

func localGmailDeadlockBackoff(attempt int) time.Duration {
	attempt = max(0, min(attempt, 5))
	return time.Duration(10*(1<<attempt))*time.Millisecond + time.Duration(rand.IntN(25+attempt*10))*time.Millisecond
}

func (s *Service) FindLocalPurchase(ctx context.Context, orderNo string) (*tradeapp.GmailPurchaseDelivery, error) {
	orderNo = strings.TrimSpace(orderNo)
	if orderNo == "" {
		return nil, ErrLocalResourceMissing
	}
	delivery, err := findLocalPurchaseWithDB(s.dbFor(ctx), orderNo)
	if err != nil {
		return nil, err
	}
	if delivery == nil {
		return nil, ErrLocalResourceMissing
	}
	return delivery, nil
}

func findLocalPurchaseWithDB(db *gorm.DB, orderNo string) (*tradeapp.GmailPurchaseDelivery, error) {
	var row struct {
		AllocationID    uint   `gorm:"column:allocation_id"`
		ResourceID      uint   `gorm:"column:resource_id"`
		SupplyScope     string `gorm:"column:supply_scope"`
		Email           string `gorm:"column:email"`
		Password        string `gorm:"column:password"`
		TwoFactorSecret string `gorm:"column:two_factor_secret"`
		AppPassword     string `gorm:"column:app_password"`
	}
	err := db.Table("gmail_allocations AS a").
		Select("a.id AS allocation_id, r.id AS resource_id, a.supply_scope, a.email, r.password, r.two_factor_secret, r.app_password").
		Joins("JOIN gmail_resources AS r ON r.id = a.resource_id").
		Where("a.order_no = ? AND a.source = ? AND a.service_mode = ? AND a.status = ?", orderNo, SourceLocal, string(tradedomain.ServiceModePurchase), AllocationStatusAllocated).
		Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("load local Gmail purchase: %w", err)
	}
	return &tradeapp.GmailPurchaseDelivery{
		AllocationID: row.AllocationID, ResourceID: row.ResourceID,
		SupplyScope: tradeapp.SupplyScope(row.SupplyScope), Email: row.Email, Password: row.Password,
		TwoFactorSecret: row.TwoFactorSecret, AppPassword: row.AppPassword,
	}, nil
}

func (s *Service) ReleaseLocalAllocation(ctx context.Context, orderNo string) error {
	orderNo = strings.TrimSpace(orderNo)
	if orderNo == "" {
		return allocdomain.ErrInvalidAllocationRequest
	}
	return s.withLocalGmailAllocationTx(ctx, func(txCtx context.Context) error {
		tx := s.dbFor(txCtx)
		var guard localAllocationGuardModel
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("order_no = ?", orderNo).Take(&guard).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("lock Gmail allocation guard for release: %w", err)
		}
		if guard.Type != "gmail" {
			return allocdomain.ErrAllocationConflict
		}
		var allocation allocationModel
		err = tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("order_no = ?", orderNo).Take(&allocation).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("lock Gmail allocation for release: %w", err)
		}
		if allocation.GuardType != "gmail" {
			return allocdomain.ErrAllocationConflict
		}
		if allocation.Status != AllocationStatusAllocated {
			return nil
		}
		now := s.now().UTC()
		result := tx.Model(&allocationModel{}).
			Where("id = ? AND status = ?", allocation.ID, AllocationStatusAllocated).
			Updates(map[string]any{"status": AllocationStatusReleased, "released_at": now})
		if result.Error != nil {
			return fmt.Errorf("release Gmail allocation: %w", result.Error)
		}
		return nil
	})
}

func (s *Service) SetLocalResourceEnabled(ctx context.Context, resourceID uint, enabled bool) error {
	return s.setLocalResourceEnabled(ctx, resourceID, 0, enabled, "", nil, false)
}

func (s *Service) SetAdminLocalResourceEnabled(
	ctx context.Context,
	resourceID uint,
	expectedVersion uint64,
	enabled bool,
	operatorUserID uint,
	idempotencyKey, requestID, path string,
) error {
	if s == nil || s.redis == nil || resourceID == 0 || expectedVersion == 0 || operatorUserID == 0 {
		return ErrInvalidLocalResource
	}
	operation := "disable"
	if enabled {
		operation = "enable"
	}
	fingerprint := stableDigest(fmt.Sprintf("%s|%d|%d|%d", operation, operatorUserID, resourceID, expectedVersion))
	reused, err := s.claimLocalGmailCommandIdempotency(ctx, operatorUserID, idempotencyKey, fingerprint)
	if err != nil {
		return err
	}
	return s.setLocalResourceEnabled(ctx, resourceID, expectedVersion, enabled, requestID, &governancedomain.OperationLog{
		OperatorUserID: operatorUserID,
		OperationType:  "gmail.resource." + operation,
		ResourceType:   "gmail_resource",
		ResourceID:     strconv.FormatUint(uint64(resourceID), 10),
		Path:           path,
		Result:         "success",
		SafeSummary:    "Gmail resource " + operation + " command applied.",
		RequestID:      requestID,
	}, reused)
}

func (s *Service) setLocalResourceEnabled(
	ctx context.Context,
	resourceID uint,
	expectedVersion uint64,
	enabled bool,
	requestID string,
	log *governancedomain.OperationLog,
	reused bool,
) error {
	if resourceID == 0 {
		return ErrLocalResourceMissing
	}
	shouldSchedule := false
	changed := false
	err := s.dbFor(ctx).Transaction(func(tx *gorm.DB) error {
		var root resourceRootModel
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND type = ?", resourceID, "gmail").Take(&root).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrLocalResourceMissing
			}
			return fmt.Errorf("lock Gmail resource state root: %w", err)
		}
		var item localResourceModel
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", resourceID).Take(&item).Error; err != nil {
			return fmt.Errorf("lock Gmail resource state: %w", err)
		}
		if item.Status == LocalResourceDeleted {
			return ErrLocalResourceMissing
		}
		targetReached := enabled && item.Status != LocalResourceDisabled || !enabled && item.Status == LocalResourceDisabled
		if reused && targetReached {
			return nil
		}
		if expectedVersion != 0 && root.Version != expectedVersion {
			return ErrLocalResourceVersion
		}
		now := s.now().UTC()
		if enabled {
			if item.Status != LocalResourceDisabled {
				return ErrInvalidLocalResource
			}
			result := tx.Model(&localResourceModel{}).Where("id = ? AND status = ?", item.ID, LocalResourceDisabled).Updates(map[string]any{
				"status": LocalResourcePending, "validation_generation": gorm.Expr("validation_generation + 1"),
				"validation_failures": 0, "validation_request_id": strings.TrimSpace(requestID),
				"validation_command_hash": "", "last_safe_error": "", "last_checked_at": nil, "updated_at": now,
			})
			if result.Error != nil {
				return result.Error
			}
			changed, shouldSchedule = result.RowsAffected == 1, result.RowsAffected == 1
		} else if item.Status != LocalResourceDisabled {
			result := tx.Model(&localResourceModel{}).Where("id = ? AND status <> ?", item.ID, LocalResourceDisabled).
				Updates(map[string]any{"status": LocalResourceDisabled, "updated_at": now})
			if result.Error != nil {
				return result.Error
			}
			changed = result.RowsAffected == 1
		}
		if !changed {
			return nil
		}
		if err := tx.Model(&resourceRootModel{}).Where("id = ? AND version = ?", root.ID, root.Version).
			Updates(map[string]any{"version": gorm.Expr("version + 1"), "updated_at": now}).Error; err != nil {
			return err
		}
		if log != nil && s.logs != nil {
			if err := s.logs.CreateInTx(ctx, tx, log); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil || !shouldSchedule {
		return err
	}
	if err := s.scheduleLocalResourceValidation(ctx, resourceID); err != nil {
		slog.Warn("schedule local Gmail validation failed", "resource_id", resourceID, "error", err)
	}
	return nil
}

func (s *Service) SetAdminLocalResourceForSale(
	ctx context.Context,
	resourceID uint,
	expectedVersion uint64,
	forSale bool,
	operatorUserID uint,
	idempotencyKey, requestID, path string,
) error {
	if s == nil || s.redis == nil || resourceID == 0 || expectedVersion == 0 || operatorUserID == 0 {
		return ErrInvalidLocalResource
	}
	operation := "unpublish"
	if forSale {
		operation = "publish"
	}
	fingerprint := stableDigest(fmt.Sprintf("%s|%d|%d|%d", operation, operatorUserID, resourceID, expectedVersion))
	reused, err := s.claimLocalGmailCommandIdempotency(ctx, operatorUserID, idempotencyKey, fingerprint)
	if err != nil {
		return err
	}
	return s.dbFor(ctx).Transaction(func(tx *gorm.DB) error {
		var root resourceRootModel
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND type = ?", resourceID, "gmail").Take(&root).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrLocalResourceMissing
			}
			return fmt.Errorf("lock Gmail publish root: %w", err)
		}
		var resource localResourceModel
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", resourceID).Take(&resource).Error; err != nil {
			return fmt.Errorf("lock Gmail publish resource: %w", err)
		}
		if resource.Status == LocalResourceDeleted {
			return ErrLocalResourceMissing
		}
		if reused && resource.ForSale == forSale {
			return nil
		}
		if root.Version != expectedVersion {
			return ErrLocalResourceVersion
		}
		if resource.ForSale == forSale {
			return nil
		}
		if forSale {
			var eligible uint
			if err := tx.Table("users").Select("id").Where("id = ? AND status = ? AND role IN ?",
				root.OwnerUserID, "active", []string{"supplier", "admin", "super_admin"}).Limit(1).Scan(&eligible).Error; err != nil {
				return fmt.Errorf("validate Gmail public owner: %w", err)
			}
			if eligible == 0 {
				return ErrInvalidLocalResource
			}
		}
		now := s.now().UTC()
		if err := tx.Model(&localResourceModel{}).Where("id = ?", resourceID).
			Updates(map[string]any{"for_sale": forSale, "updated_at": now}).Error; err != nil {
			return err
		}
		if err := tx.Model(&resourceRootModel{}).Where("id = ? AND version = ?", root.ID, root.Version).
			Updates(map[string]any{"version": gorm.Expr("version + 1"), "updated_at": now}).Error; err != nil {
			return err
		}
		if s.logs != nil {
			return s.logs.CreateInTx(ctx, tx, &governancedomain.OperationLog{
				OperatorUserID: operatorUserID, OperationType: "gmail.resource." + operation,
				ResourceType: "gmail_resource", ResourceID: strconv.FormatUint(uint64(resourceID), 10),
				Path: path, Result: "success", SafeSummary: "Gmail resource " + operation + " command applied.",
				RequestID: requestID,
			})
		}
		return nil
	})
}

func isLocalResourceStatus(status string) bool {
	switch status {
	case LocalResourcePending, LocalResourceValidating, LocalResourceIdentifying, LocalResourceNormal, LocalResourceAbnormal, LocalResourceDisabled:
		return true
	default:
		return false
	}
}
