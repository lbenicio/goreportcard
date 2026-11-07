package handlers

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dgraph-io/badger/v2"
)

func assertMaintainerUI(t *testing.T, body string) {
	t.Helper()
	for _, want := range []string{
		`href="https://github.com/lbenicio/goreportcard">GitHub</a>`,
		`Go Report Card</strong> is maintained by <a href="https://github.com/lbenicio">lbenicio</a>`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("page does not contain %q", want)
		}
	}
	for _, removed := range []string{"Supporters", "Patreon", "Sponsored", "gojp/goreportcard", "shawnps", "ironzeb", "Production Go"} {
		if strings.Contains(body, removed) {
			t.Errorf("page still contains removed content %q", removed)
		}
	}
}

func TestHomePageUsesMaintainerUI(t *testing.T) {
	db, err := badger.Open(badger.DefaultOptions("").WithInMemory(true).WithLogger(nil))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	handler := GRCHandler{AssetsFS: http.Dir("../assets")}
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	response := httptest.NewRecorder()
	handler.HomeHandler(response, request, db)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", response.Code, http.StatusOK, response.Body.String())
	}
	assertMaintainerUI(t, response.Body.String())
	if !strings.Contains(response.Body.String(), "Recently Generated") {
		t.Error("homepage is missing the recently generated section")
	}
}

func TestSupportersPageReturnsNotFound(t *testing.T) {
	handler := GRCHandler{AssetsFS: http.Dir("../assets")}
	request := httptest.NewRequest(http.MethodGet, "/supporters", nil)
	response := httptest.NewRecorder()
	handler.HomeHandler(response, request, nil)

	if response.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusNotFound)
	}
	if strings.Contains(response.Body.String(), "Supporters") {
		t.Error("removed supporters page still appears in the 404 response")
	}
}

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
	assertMaintainerUI(t, response.Body.String())
	for _, want := range []string{"var forceRefresh =  true ", "data.pending", "refresh=1", "{{message}}"} {
		if !strings.Contains(response.Body.String(), want) {
			t.Errorf("report HTML does not contain %q", want)
		}
	}
	if strings.Contains(response.Body.String(), "{{{message}}}") {
		t.Error("report template renders alert messages as unescaped HTML")
	}
}
