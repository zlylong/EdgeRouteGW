package main

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestDecodeStrictJSON_RejectUnknownField(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("POST", "/", strings.NewReader(`{"mode":"A","extra":1}`))

	var req struct {
		Mode string `json:"mode"`
	}
	if err := decodeStrictJSON(c, &req, false); err == nil {
		t.Fatalf("expected unknown field error, got nil")
	}
}

func TestDecodeStrictJSON_AllowEmptyBody(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("POST", "/", strings.NewReader(""))

	var req struct {
		Xray *bool `json:"xray"`
	}
	if err := decodeStrictJSON(c, &req, true); err != nil {
		t.Fatalf("expected nil for empty body when allowEmptyBody=true, got %v", err)
	}
}
