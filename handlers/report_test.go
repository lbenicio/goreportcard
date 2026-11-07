package handlers

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dgraph-io/badger/v2"
)

func TestReportHandlerRendersPollingTemplate(t *testing.T) {
	db, err := badger.Open(badger.DefaultOptions("").WithInMemory(true).WithLogger(nil))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	handler := GRCHandler{AssetsFS: http.Dir("../assets")}
	request := httptest.NewRequest(http.MethodGet, "/report/github.com/example/repo?wait=1", nil)
	response := httptest.NewRecorder()
	handler.ReportHandler(response, request, db, "github.com/example/repo")

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", response.Code, http.StatusOK, response.Body.String())
	}
	for _, want := range []string{"var forceRefresh =  true ", "data.pending", "refresh=1", "{{message}}"} {
		if !strings.Contains(response.Body.String(), want) {
			t.Errorf("report HTML does not contain %q", want)
		}
	}
	if strings.Contains(response.Body.String(), "{{{message}}}") {
		t.Error("report template renders alert messages as unescaped HTML")
	}
}
