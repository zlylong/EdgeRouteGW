package main

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
)

// Error codes shared by the JSON API. The body keeps the "error" key the UI
// already reads and adds a stable "error_code" clients can switch on.
const (
	errCodeNotFound   = "NOT_FOUND"
	errCodeBadRequest = "BAD_REQUEST"
	errCodeInternal   = "INTERNAL_ERROR"
)

// apiError writes the standard error envelope. Internal details belong in
// the journal, not in the response; callers log err themselves.
func apiError(c *gin.Context, status int, code, msg string) {
	c.JSON(status, gin.H{"success": false, "error": msg, "error_code": code})
}

func apiNotFound(c *gin.Context, what string) {
	apiError(c, http.StatusNotFound, errCodeNotFound, what+" not found")
}

// pageWindow is an optional ?limit=&offset= window over a list response.
// Lists keep their existing shape (a bare array or the same envelope); when
// limit is present the response also carries X-Total-Count with the size of
// the unpaged list so a client can page without a second call.
type pageWindow struct {
	limit  int
	offset int
	active bool
}

const pageMaxLimit = 1000

func parsePageWindow(c *gin.Context) (pageWindow, bool) {
	rawLimit := strings.TrimSpace(c.Query("limit"))
	rawOffset := strings.TrimSpace(c.Query("offset"))
	if rawLimit == "" && rawOffset == "" {
		return pageWindow{}, true
	}
	w := pageWindow{active: true, limit: pageMaxLimit}
	if rawLimit != "" {
		n, err := strconv.Atoi(rawLimit)
		if err != nil || n < 1 {
			apiError(c, http.StatusBadRequest, errCodeBadRequest, "limit must be a positive integer")
			return w, false
		}
		if n > pageMaxLimit {
			n = pageMaxLimit
		}
		w.limit = n
	}
	if rawOffset != "" {
		n, err := strconv.Atoi(rawOffset)
		if err != nil || n < 0 {
			apiError(c, http.StatusBadRequest, errCodeBadRequest, "offset must be a non-negative integer")
			return w, false
		}
		w.offset = n
	}
	return w, true
}

// apply slices items to the window and sets X-Total-Count when paging.
func pageSlice[T any](c *gin.Context, w pageWindow, items []T) []T {
	if !w.active {
		return items
	}
	c.Header("X-Total-Count", strconv.Itoa(len(items)))
	if w.offset >= len(items) {
		return items[:0]
	}
	end := w.offset + w.limit
	if end > len(items) {
		end = len(items)
	}
	return items[w.offset:end]
}
