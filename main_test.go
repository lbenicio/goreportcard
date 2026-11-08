package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/lbenicio/goreportcard/storage"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

type mainTestStore struct{}

func (*mainTestStore) GetReport(context.Context, string) (storage.Report, error) {
	return storage.Report{}, nil
}

func (*mainTestStore) SaveReport(context.Context, storage.Report) error { return nil }
func (*mainTestStore) AddRecent(context.Context, string) error          { return nil }
func (*mainTestStore) RecentRepos(context.Context) ([]string, error)    { return nil, nil }
func (*mainTestStore) HighScores(context.Context) (int64, []storage.HighScore, error) {
	return 0, nil, nil
}

func TestMakeHandlerPassesCanonicalRepositoryToHandler(t *testing.T) {
	store := &mainTestStore{}
	var gotStore storage.Store
	var gotRepo string
	handler := makeHandler(store, "report", func(_ http.ResponseWriter, _ *http.Request, db storage.Store, repo string) {
		gotStore = db
		gotRepo = repo
	})

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/report/github.com/example/project", nil))

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusOK)
	}
	if gotStore != store {
		t.Fatal("handler received a different store")
	}
	if gotRepo != "github.com/example/project" {
		t.Fatalf("repository = %q, want %q", gotRepo, "github.com/example/project")
	}
}

func TestMakeHandlerRedirectsLegacyRepositoryURL(t *testing.T) {
	called := false
	handler := makeHandler(nil, "report", func(http.ResponseWriter, *http.Request, storage.Store, string) {
		called = true
	})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/report/example/project", nil))

	if response.Code != http.StatusMovedPermanently {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusMovedPermanently)
	}
	if got, want := response.Header().Get("Location"), "/report/github.com/example/project"; got != want {
		t.Fatalf("redirect location = %q, want %q", got, want)
	}
	if called {
		t.Fatal("legacy URL invoked the report handler instead of redirecting")
	}
}

func TestMakeHandlerRejectsUnmatchedPath(t *testing.T) {
	called := false
	handler := makeHandler(nil, "report", func(http.ResponseWriter, *http.Request, storage.Store, string) {
		called = true
	})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/other/example/project", nil))

	if response.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusNotFound)
	}
	if called {
		t.Fatal("unmatched path invoked the report handler")
	}
}

func TestInjectStoreHandlerPassesConfiguredStore(t *testing.T) {
	store := &mainTestStore{}
	var gotStore storage.Store
	handler := injectStoreHandler(store, func(_ http.ResponseWriter, _ *http.Request, db storage.Store) {
		gotStore = db
	})
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/checks", nil))

	if gotStore != store {
		t.Fatal("handler received a different store")
	}
}

func TestInstrumentRecordsResponseMetric(t *testing.T) {
	m := setupMetrics()
	t.Cleanup(func() { prometheus.Unregister(m.responseTimes) })

	path, handler := m.instrument("/checks", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	if path != "/checks" {
		t.Fatalf("instrumented path = %q, want %q", path, "/checks")
	}
	response := httptest.NewRecorder()
	handler(response, httptest.NewRequest(http.MethodGet, "/checks", nil))
	if response.Code != http.StatusNoContent {
		t.Fatalf("response status = %d, want %d", response.Code, http.StatusNoContent)
	}

	metricsResponse := httptest.NewRecorder()
	promhttp.Handler().ServeHTTP(metricsResponse, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if metricsResponse.Code != http.StatusOK {
		t.Fatalf("metrics response status = %d, want %d", metricsResponse.Code, http.StatusOK)
	}
	if !strings.Contains(metricsResponse.Body.String(), `response_time_ms_count{endpoint="/checks"} 1`) {
		t.Fatalf("metrics response does not contain one /checks observation: %s", metricsResponse.Body.String())
	}
}
