package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestParsePaginationDefaultsAndClampsLimit(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(RequestID())
	router.GET("/items", func(c *gin.Context) {
		offset, limit, ok := ParsePagination(c, PaginationOptions{
			DefaultLimit: 20,
			MaxLimit:     100,
		})
		require.True(t, ok)
		c.JSON(http.StatusOK, gin.H{"offset": offset, "limit": limit})
	})

	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/items?limit=10000", nil))

	require.Equal(t, http.StatusOK, response.Code)
	require.JSONEq(t, `{"offset":0,"limit":100}`, response.Body.String())
}

func TestParsePaginationRejectsInvalidValues(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cases := []string{
		"/items?offset=-1",
		"/items?offset=abc",
		"/items?limit=0",
		"/items?limit=-1",
		"/items?limit=abc",
	}

	for _, path := range cases {
		t.Run(path, func(t *testing.T) {
			router := gin.New()
			router.Use(RequestID())
			router.GET("/items", func(c *gin.Context) {
				_, _, ok := ParsePagination(c, PaginationOptions{
					DefaultLimit: 20,
					MaxLimit:     100,
				})
				require.False(t, ok)
			})

			response := httptest.NewRecorder()
			router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))

			require.Equal(t, http.StatusBadRequest, response.Code)
		})
	}
}
