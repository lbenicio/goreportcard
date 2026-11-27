package handlers

import (
	"context"
	"strings"
	"testing"
)

func TestNewChecksRespRejectsInvalidRepoPath(t *testing.T) {
	_, err := newChecksResp(context.Background(), nil, "../../outside", true)
	if err == nil || !strings.Contains(err.Error(), "invalid module path") {
		t.Fatalf("newChecksResp error = %v, want invalid module path error", err)
	}
}
