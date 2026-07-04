package msacl

import (
	"fmt"
	"runtime"
	"strings"
	"sync"
)

var (
	taskContextMu sync.Mutex
	taskContexts  = map[uint64]string{}
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

func writeLog(_ string, _ string, _ ...any) {
}
