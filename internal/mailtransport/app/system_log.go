package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	governanceapp "github.com/donnel666/remail/internal/governance/app"
	governancedomain "github.com/donnel666/remail/internal/governance/domain"
)

type SystemLogPort = governanceapp.SystemLogPort

func writeSystemLog(ctx context.Context, logs SystemLogPort, level, eventType, requestID, bizType, bizID, message string, detail any) {
	if logs == nil {
		return
	}
	_ = logs.Create(ctx, &governancedomain.SystemLog{
		Level:     level,
		Module:    "mailtransport",
		EventType: eventType,
		RequestID: requestID,
		BizType:   bizType,
		BizID:     bizID,
		Message:   message,
		Detail:    safeDiagnostic(fmt.Sprint(detail)),
	})
}

func mailLogID(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])[:16]
}
