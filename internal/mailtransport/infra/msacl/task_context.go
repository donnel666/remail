package msacl

import (
	"fmt"
	"log/slog"
	"regexp"
	"runtime"
	"strings"
	"sync"
)

var (
	taskContextMu sync.Mutex
	taskContexts  = map[uint64]string{}

	msaclDebugLogs = envBool("MSACL_DEBUG_LOGS", false)

	logEmailPattern               = regexp.MustCompile(`([A-Za-z0-9._%+\-])[A-Za-z0-9._%+\-]*@([A-Za-z0-9.\-]+\.[A-Za-z]{2,})`)
	logCodePattern                = regexp.MustCompile(`\b\d{4,8}\b`)
	logTokenPattern               = regexp.MustCompile(`[A-Za-z0-9+/_%=-]{80,}`)
	logSensitiveAssignmentPattern = regexp.MustCompile(`(?i)\b(password|passwd|pwd|refresh[_-]?token|access[_-]?token|id[_-]?token|device[_-]?code|user[_-]?code|flowtoken|vanguardflowtoken|ppft|sft|canary|iotttext|iproofoptions|authorization|cookie|set-cookie|client[_-]?secret|sessionid|uaid|opid|route)(["']?\s*[:=]\s*)(["']?)[^,\s;&}\]"]+`)
	logSensitiveQueryPattern      = regexp.MustCompile(`(?i)([?&](?:code|token|slt|sft|ppft|canary|route|uaid|opid|client-request-id|correlationid|sessionid|client_secret|flowtoken|vanguardflowtoken|device_code|user_code|access_token|refresh_token|id_token)=)[^&\s]+`)
)

func currentGID() uint64 {
	var buf [64]byte
	n := runtime.Stack(buf[:], false)
	line := strings.Fields(string(buf[:n]))
	if len(line) < 2 {
		return 0
	}
	var id uint64
	_, _ = fmt.Sscanf(line[1], "%d", &id)
	return id
}

func getTaskLogID() string {
	taskContextMu.Lock()
	defer taskContextMu.Unlock()
	return taskContexts[currentGID()]
}

func setTaskLogID(taskLogID string) string {
	gid := currentGID()
	taskContextMu.Lock()
	defer taskContextMu.Unlock()
	previous := taskContexts[gid]
	if taskLogID == "" {
		delete(taskContexts, gid)
	} else {
		taskContexts[gid] = taskLogID
	}
	return previous
}

func logDebug(format string, args ...any) {
	if !msaclDebugLogs {
		return
	}
	writeLog("DEBUG", format, args...)
}

func logInfo(format string, args ...any) {
	writeLog("INFO", format, args...)
}

func logWarning(format string, args ...any) {
	writeLog("WARNING", format, args...)
}

func logError(format string, args ...any) {
	writeLog("ERROR", format, args...)
}

func writeLog(level string, format string, args ...any) {
	message := sanitizeLogMessage(fmt.Sprintf(format, args...))
	attrs := []any{"component", "msacl"}
	if taskLogID := getTaskLogID(); taskLogID != "" {
		attrs = append(attrs, "task_log_id", taskLogID)
	}
	switch level {
	case "DEBUG":
		slog.Debug(message, attrs...)
	case "WARNING":
		slog.Warn(message, attrs...)
	case "ERROR":
		slog.Error(message, attrs...)
	default:
		slog.Info(message, attrs...)
	}
}

func sanitizeLogMessage(value string) string {
	value = redactSensitiveAssignments(value)
	value = logSensitiveQueryPattern.ReplaceAllString(value, "${1}[redacted]")
	value = logEmailPattern.ReplaceAllString(value, "$1***@$2")
	value = logTokenPattern.ReplaceAllString(value, "[token]")
	value = logCodePattern.ReplaceAllString(value, "[code]")
	return strings.TrimSpace(value)
}

func redactSensitiveAssignments(value string) string {
	return logSensitiveAssignmentPattern.ReplaceAllStringFunc(value, func(match string) string {
		parts := logSensitiveAssignmentPattern.FindStringSubmatch(match)
		if len(parts) < 4 {
			return "[redacted]"
		}
		return parts[1] + parts[2] + parts[3] + "[redacted]"
	})
}
