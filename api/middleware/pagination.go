package middleware

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
)

type PaginationOptions struct {
	DefaultLimit int
	MaxLimit     int
}

func ParsePagination(c *gin.Context, options PaginationOptions) (int, int, bool) {
	defaultLimit := options.DefaultLimit
	if defaultLimit <= 0 {
		defaultLimit = 20
	}
	maxLimit := options.MaxLimit
	if maxLimit <= 0 {
		maxLimit = defaultLimit
	}

	offset, ok := parseNonNegativeIntQuery(c, "offset", 0)
	if !ok {
		return 0, 0, false
	}
	limit, ok := parsePositiveIntQuery(c, "limit", defaultLimit)
	if !ok {
		return 0, 0, false
	}
	if limit > maxLimit {
		limit = maxLimit
	}
	return offset, limit, true
}

func parseNonNegativeIntQuery(c *gin.Context, name string, fallback int) (int, bool) {
	value := strings.TrimSpace(c.Query(name))
	if value == "" {
		return fallback, true
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed < 0 {
		writePaginationError(c)
		return 0, false
	}
	return parsed, true
}

func parsePositiveIntQuery(c *gin.Context, name string, fallback int) (int, bool) {
	value := strings.TrimSpace(c.Query(name))
	if value == "" {
		return fallback, true
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed <= 0 {
		writePaginationError(c)
		return 0, false
	}
	return parsed, true
}

func writePaginationError(c *gin.Context) {
	c.JSON(http.StatusBadRequest, gin.H{
		"message":   "Invalid query parameters.",
		"requestId": GetRequestID(c),
	})
}
