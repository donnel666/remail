package infra

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/donnel666/remail/internal/mailmatch/domain"
	"github.com/go-sql-driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type MessageProjectionModel struct {
	MessageID         uint      `gorm:"primaryKey;autoIncrement:false;column:message_id"`
	MatchedOrderID    *uint     `gorm:"column:matched_order_id"`
	Status            string    `gorm:"type:varchar(32);not null"`
	VerificationCode  string    `gorm:"type:varchar(64);not null;default:'';column:verification_code"`
	MatchDiagnostic   string    `gorm:"type:varchar(500);not null;default:'';column:match_diagnostic"`
	MessageReceivedAt time.Time `gorm:"not null;column:message_received_at"`
	DecidedAt         time.Time `gorm:"not null;autoCreateTime:milli;column:decided_at"`
}

func (MessageProjectionModel) TableName() string { return "mailmatch_message_projections" }

// AppendMessages stores each message identity once and returns the persisted
// facts in caller order. Duplicate identities never update the existing row.
// Callers must not wrap the append phase in a parent database transaction.
func (r *Repo) AppendMessages(ctx context.Context, messages []domain.Message) (stored []domain.Message, inserted int, err error) {
	defer func() { recordMailmatchAutocommitContention(ctx, err) }()
	if len(messages) == 0 {
		return nil, 0, nil
	}
	storedByIdentity, err := r.findMessagesByIdentity(ctx, messages)
	if err != nil {
		return nil, 0, err
	}
	models := make([]MessageModel, len(messages))
	for i := range messages {
		models[i] = messageModelFromDomain(messages[i])
		// The fact table contains only provider-observed content. Matching
		// ownership is published separately after the authoritative fence.
		models[i].MatchedOrderID = nil
		models[i].VerificationCode = ""
		models[i].Status = string(domain.MessageStatusReceived)
		models[i].MatchDiagnostic = ""
	}
	sort.SliceStable(models, func(i, j int) bool {
		if models[i].EmailResourceID != models[j].EmailResourceID {
			return models[i].EmailResourceID < models[j].EmailResourceID
		}
		return models[i].DedupeKey < models[j].DedupeKey
	})
	for i := range models {
		if _, ok := storedByIdentity[messageIdentity(models[i].EmailResourceID, models[i].DedupeKey)]; ok {
			continue
		}
		created := r.dbFor(ctx).
			Session(&gorm.Session{SkipDefaultTransaction: true}).
			Create(&models[i])
		if created.Error != nil {
			var mysqlErr *mysql.MySQLError
			if errors.Is(created.Error, gorm.ErrDuplicatedKey) ||
				(errors.As(created.Error, &mysqlErr) && mysqlErr.Number == 1062) {
				storedByIdentity[messageIdentity(models[i].EmailResourceID, models[i].DedupeKey)] = domain.Message{}
				continue
			}
			return nil, 0, fmt.Errorf("append mailmatch message: %w", created.Error)
		}
		inserted += int(created.RowsAffected)
		storedByIdentity[messageIdentity(models[i].EmailResourceID, models[i].DedupeKey)] = domain.Message{}
	}
	stored, err = r.resolveAppendedMessages(ctx, messages)
	return stored, inserted, err
}

func (r *Repo) findMessagesByIdentity(ctx context.Context, messages []domain.Message) (map[string]domain.Message, error) {
	storedByIdentity := make(map[string]domain.Message, len(messages))
	dedupeKeysByResource := make(map[uint][]string)
	for i := range messages {
		dedupeKeysByResource[messages[i].EmailResourceID] = append(
			dedupeKeysByResource[messages[i].EmailResourceID],
			messages[i].DedupeKey,
		)
	}
	for resourceID, dedupeKeys := range dedupeKeysByResource {
		var rows []MessageModel
		if err := r.dbFor(ctx).Model(&MessageModel{}).
			Where("email_resource_id = ? AND dedupe_key IN ?", resourceID, dedupeKeys).
			Find(&rows).Error; err != nil {
			return nil, fmt.Errorf("find appended mailmatch messages: %w", err)
		}
		for _, row := range rows {
			item := messageModelToDomain(row)
			storedByIdentity[messageIdentity(item.EmailResourceID, item.DedupeKey)] = item
		}
	}
	return storedByIdentity, nil
}

func (r *Repo) resolveAppendedMessages(ctx context.Context, messages []domain.Message) ([]domain.Message, error) {
	storedByIdentity, err := r.findMessagesByIdentity(ctx, messages)
	if err != nil {
		return nil, err
	}
	stored := make([]domain.Message, len(messages))
	for i := range messages {
		item, ok := storedByIdentity[messageIdentity(messages[i].EmailResourceID, messages[i].DedupeKey)]
		if !ok || item.ID == 0 {
			return nil, fmt.Errorf("resolve appended mailmatch message: %w", domain.ErrMessageNotFound)
		}
		stored[i] = item
	}
	return stored, nil
}

// ListUnprojectedMessages returns the newest facts whose matching decision was
// not committed. A later fetch for the same resource replays these rows even
// when Microsoft no longer includes them in its newest-message response.
func (r *Repo) ListUnprojectedMessages(ctx context.Context, resourceType domain.ResourceType, emailResourceIDs []uint, limit int) ([]domain.Message, error) {
	if (resourceType != domain.ResourceTypeMicrosoft && resourceType != domain.ResourceTypeDomain) || len(emailResourceIDs) == 0 || limit <= 0 {
		return nil, nil
	}
	var rows []MessageModel
	if err := r.dbFor(ctx).
		Table("mailmatch_messages AS m").
		Select("m.*").
		Joins("LEFT JOIN mailmatch_message_projections AS mp ON mp.message_id = m.id").
		Where("m.email_resource_id IN ? AND m.resource_type = ? AND mp.message_id IS NULL", emailResourceIDs, string(resourceType)).
		Order("m.received_at DESC, m.id DESC").
		Limit(limit).
		Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("list unprojected mailmatch messages: %w", err)
	}
	messages := make([]domain.Message, len(rows))
	for i := range rows {
		messages[i] = messageModelToDomain(rows[i])
	}
	return messages, nil
}

// InsertMessageProjections advances the matching decision for each persisted
// message and returns the authoritative decision in caller order. A message
// may move between non-matched states and may be promoted to matched; matched
// ownership is terminal so later pickup cannot steal an already delivered mail.
// The single-row, message-ID order is safe both in autocommit and in the short
// projection+delivery transaction used by realtime ingestion.
func (r *Repo) InsertMessageProjections(ctx context.Context, messages []domain.Message) (projected []domain.Message, newlyMatchedIDs []uint, err error) {
	defer func() { recordMailmatchAutocommitContention(ctx, err) }()
	if len(messages) == 0 {
		return nil, nil, nil
	}
	models := make([]MessageProjectionModel, len(messages))
	requestedIDs := make(map[uint]struct{}, len(messages))
	for i := range messages {
		if messages[i].ID == 0 || messages[i].ReceivedAt.IsZero() {
			return nil, nil, domain.ErrInvalidRequest
		}
		models[i] = MessageProjectionModel{
			MessageID:         messages[i].ID,
			MatchedOrderID:    messages[i].MatchedOrderID,
			Status:            string(messages[i].Status),
			VerificationCode:  truncate(messages[i].VerificationCode, 64),
			MatchDiagnostic:   truncate(messages[i].MatchDiagnostic, 500),
			MessageReceivedAt: messages[i].ReceivedAt.UTC(),
		}
		requestedIDs[messages[i].ID] = struct{}{}
	}
	sort.SliceStable(models, func(i, j int) bool { return models[i].MessageID < models[j].MessageID })
	newlyMatched := make(map[uint]struct{}, len(models))
	for i := range models {
		db := r.dbFor(ctx).Session(&gorm.Session{SkipDefaultTransaction: true})
		created := db.
			Clauses(clause.Insert{Modifier: "IGNORE"}).
			Create(&models[i])
		if created.Error != nil {
			return nil, nil, fmt.Errorf("insert mailmatch message projection: %w", created.Error)
		}
		if created.RowsAffected == 1 && models[i].Status == string(domain.MessageStatusMatched) {
			newlyMatched[models[i].MessageID] = struct{}{}
		}
		updated := db.Model(&MessageProjectionModel{}).
			Where("message_id = ? AND status <> ?", models[i].MessageID, string(domain.MessageStatusMatched)).
			Updates(map[string]any{
				"matched_order_id":    models[i].MatchedOrderID,
				"status":              models[i].Status,
				"verification_code":   models[i].VerificationCode,
				"match_diagnostic":    models[i].MatchDiagnostic,
				"message_received_at": models[i].MessageReceivedAt,
				"decided_at":          time.Now().UTC(),
			})
		if updated.Error != nil {
			return nil, nil, fmt.Errorf("advance mailmatch message projection: %w", updated.Error)
		}
		if updated.RowsAffected == 1 && models[i].Status == string(domain.MessageStatusMatched) {
			newlyMatched[models[i].MessageID] = struct{}{}
		}
	}
	stored, err := r.findMessageProjections(ctx, requestedIDs)
	if err != nil {
		return nil, nil, err
	}
	if len(stored) != len(requestedIDs) {
		return nil, nil, fmt.Errorf("resolve mailmatch message projection: %w", domain.ErrMessageNotFound)
	}
	projected = make([]domain.Message, len(messages))
	for i := range messages {
		projection, ok := stored[messages[i].ID]
		if !ok {
			return nil, nil, fmt.Errorf("resolve mailmatch message projection: %w", domain.ErrMessageNotFound)
		}
		projected[i] = applyMessageProjection(messages[i], projection)
	}
	newlyMatchedIDs = make([]uint, 0, len(newlyMatched))
	for id := range newlyMatched {
		newlyMatchedIDs = append(newlyMatchedIDs, id)
	}
	sort.Slice(newlyMatchedIDs, func(i, j int) bool { return newlyMatchedIDs[i] < newlyMatchedIDs[j] })
	return projected, newlyMatchedIDs, nil
}

func (r *Repo) findMessageProjections(ctx context.Context, requestedIDs map[uint]struct{}) (map[uint]MessageProjectionModel, error) {
	ids := make([]uint, 0, len(requestedIDs))
	for id := range requestedIDs {
		ids = append(ids, id)
	}
	var rows []MessageProjectionModel
	if err := r.dbFor(ctx).Model(&MessageProjectionModel{}).
		Where("message_id IN ?", ids).
		Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("find mailmatch message projections: %w", err)
	}
	found := make(map[uint]MessageProjectionModel, len(rows))
	for _, row := range rows {
		found[row.MessageID] = row
	}
	return found, nil
}

func applyMessageProjection(message domain.Message, projection MessageProjectionModel) domain.Message {
	message.MatchedOrderID = projection.MatchedOrderID
	message.Status = domain.MessageStatus(projection.Status)
	message.VerificationCode = projection.VerificationCode
	message.MatchDiagnostic = projection.MatchDiagnostic
	return message
}

const effectiveMessageOwnerSQL = `CASE
    WHEN mp.message_id IS NULL THEN m.matched_order_id
    ELSE mp.matched_order_id
END`

func projectedMessageColumns(includeRawBody bool) string {
	return messageColumns(
		includeRawBody,
		effectiveMessageOwnerSQL,
		"CASE WHEN mp.message_id IS NULL THEN m.verification_code ELSE mp.verification_code END",
		"CASE WHEN mp.message_id IS NULL THEN m.status ELSE mp.status END",
		"CASE WHEN mp.message_id IS NULL THEN m.match_diagnostic ELSE mp.match_diagnostic END",
	)
}

func projectionOwnedMessageColumns(includeRawBody bool) string {
	return messageColumns(includeRawBody, "mp.matched_order_id", "mp.verification_code", "mp.status", "mp.match_diagnostic")
}

func legacyMessageColumns(includeRawBody bool) string {
	return messageColumns(includeRawBody, "m.matched_order_id", "m.verification_code", "m.status", "m.match_diagnostic")
}

func messageColumns(includeRawBody bool, owner, verificationCode, status, diagnostic string) string {
	columns := []string{
		"m.id",
		"m.email_resource_id",
		"m.resource_type",
		owner + " AS matched_order_id",
		"m.recipient",
		"m.recipients_json",
		"m.sender",
		"m.subject",
	}
	if includeRawBody {
		columns = append(columns, "m.raw_body")
	}
	columns = append(columns,
		"m.body_preview",
		verificationCode+" AS verification_code",
		"m.message_id_header",
		"m.provider_message_id",
		"m.dedupe_key",
		"m.protocol",
		"m.folder",
		status+" AS status",
		diagnostic+" AS match_diagnostic",
		"m.received_at",
		"m.created_at",
		"m.updated_at",
	)
	return strings.Join(columns, ", ")
}
