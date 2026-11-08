package handlers

import (
	"archive/zip"
	"bytes"
	"container/heap"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/lbenicio/goreportcard/check"
	"github.com/lbenicio/goreportcard/storage"
)

type coverageStore struct {
	testStore
	getErr        error
	saveErr       error
	addRecentErr  error
	recentErr     error
	highScoresErr error
}

func (s *coverageStore) GetReport(ctx context.Context, repo string) (storage.Report, error) {
	if s.getErr != nil {
		return storage.Report{}, s.getErr
	}
	return s.testStore.GetReport(ctx, repo)
}

func (s *coverageStore) SaveReport(ctx context.Context, report storage.Report) error {
	if s.saveErr != nil {
		return s.saveErr
	}
	return s.testStore.SaveReport(ctx, report)
}

func (s *coverageStore) AddRecent(ctx context.Context, repo string) error {
	if s.addRecentErr != nil {
		return s.addRecentErr
	}
	return s.testStore.AddRecent(ctx, repo)
}

func (s *coverageStore) RecentRepos(ctx context.Context) ([]string, error) {
	if s.recentErr != nil {
		return nil, s.recentErr
	}
	return s.testStore.RecentRepos(ctx)
}

func (s *coverageStore) HighScores(ctx context.Context) (int64, []storage.HighScore, error) {
	if s.highScoresErr != nil {
		return 0, nil, s.highScoresErr
	}
	return s.testStore.HighScores(ctx)
}

func TestGetFromCacheReportsMissingAndStoreErrors(t *testing.T) {
	_, err := getFromCache(context.Background(), &coverageStore{}, "github.com/example/missing")
	var missing notFoundError
	if !errors.As(err, &missing) {
		t.Fatalf("missing report error = %v, want notFoundError", err)
	}
	if !errors.Is(err, storage.ErrReportNotFound) {
		t.Fatalf("missing report error = %v, want ErrReportNotFound", err)
	}
	if got, want := err.Error(), `"github.com/example/missing" not found in cache`; got != want {
		t.Errorf("missing report error text = %q, want %q", got, want)
	}

	storeErr := errors.New("database unavailable")
	_, err = getFromCache(context.Background(), &coverageStore{getErr: storeErr}, "github.com/example/repo")
	if !errors.Is(err, storeErr) || !strings.Contains(err.Error(), `could not read cached report "github.com/example/repo"`) {
		t.Fatalf("store error = %v, want wrapped database error", err)
	}
}

func TestGetFromCacheNormalizesReport(t *testing.T) {
	refreshed := time.Date(2025, time.March, 4, 12, 30, 0, 0, time.FixedZone("offset", -5*60*60))
	store := &coverageStore{testStore: testStore{reports: map[string]storage.Report{
		"github.com/example/repo": {
			Repo:        "github.com/example/repo",
			Average:     0.75,
			LastRefresh: refreshed,
		},
	}}}

	got, err := getFromCache(context.Background(), store, "github.com/example/repo")
	if err != nil {
		t.Fatal(err)
	}
	if got.LastRefresh.Location() != time.UTC || !got.LastRefresh.Equal(refreshed) {
		t.Errorf("LastRefresh = %v, want UTC representation of %v", got.LastRefresh, refreshed)
	}
	if got.LastRefreshFormatted != got.LastRefresh.Format(time.UnixDate) {
		t.Errorf("formatted refresh = %q, want %q", got.LastRefreshFormatted, got.LastRefresh.Format(time.UnixDate))
	}
	if got.LastRefreshHumanized == "" {
		t.Error("humanized refresh time is empty")
	}
	if got.Grade != check.GradeFromPercentage(got.Average*100) {
		t.Errorf("grade = %q, want grade derived from average", got.Grade)
	}
}

func TestNewChecksRespUsesCacheAndPropagatesCacheErrors(t *testing.T) {
	report := storage.Report{Repo: "github.com/example/repo", Average: 0.9, Grade: check.GradeA}
	store := &coverageStore{testStore: testStore{reports: map[string]storage.Report{report.Repo: report}}}
	got, err := newChecksResp(context.Background(), store, report.Repo, false)
	if err != nil {
		t.Fatal(err)
	}
	if got.Repo != report.Repo || got.Grade != report.Grade {
		t.Errorf("cached report = %+v, want repo %q and grade %q", got, report.Repo, report.Grade)
	}

	storeErr := errors.New("database unavailable")
	_, err = newChecksResp(context.Background(), &coverageStore{getErr: storeErr}, report.Repo, false)
	if !errors.Is(err, storeErr) {
		t.Errorf("newChecksResp error = %v, want wrapped cache error", err)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func proxyResponse(request *http.Request, status int, body string) *http.Response {
	return &http.Response{
		Status:     http.StatusText(status),
		StatusCode: status,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    request,
	}
}

func setHTTPTransport(t *testing.T, transport http.RoundTripper) {
	t.Helper()
	previous := http.DefaultTransport
	http.DefaultTransport = transport
	t.Cleanup(func() { http.DefaultTransport = previous })
}

func makeModuleZip(t *testing.T, modulePath, version string, includeGoFile bool) []byte {
	t.Helper()
	var archive bytes.Buffer
	writer := zip.NewWriter(&archive)
	files := map[string]string{
		"go.mod": "module " + modulePath + "\n\ngo 1.27.3\n",
	}
	if includeGoFile {
		files["sample.go"] = "package sample\n\nfunc Sample() {}\n"
	}
	for name, content := range files {
		file, err := writer.Create(modulePath + "@" + version + "/" + name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(file, content); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return archive.Bytes()
}

func makeModuleZipWithReadOnlyDirectory(t *testing.T, modulePath, version string) []byte {
	t.Helper()
	var archive bytes.Buffer
	writer := zip.NewWriter(&archive)
	prefix := modulePath + "@" + version + "/"
	directory := &zip.FileHeader{Name: prefix + "readonly/"}
	directory.SetMode(os.ModeDir | 0o555)
	if _, err := writer.CreateHeader(directory); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"go.mod":             "module " + modulePath + "\n\ngo 1.27.3\n",
		"readonly/README.md": "no Go source files\n",
	}
	for name, content := range files {
		file, err := writer.Create(prefix + name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(file, content); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return archive.Bytes()
}

func bytesProxyResponse(request *http.Request, status int, body []byte) *http.Response {
	response := proxyResponse(request, status, "")
	response.Body = io.NopCloser(bytes.NewReader(body))
	return response
}

func moduleProxyTransport(t *testing.T, modulePath, version string, archive []byte) roundTripFunc {
	t.Helper()
	return roundTripFunc(func(request *http.Request) (*http.Response, error) {
		switch request.URL.Path {
		case "/" + modulePath + "/@latest":
			return proxyResponse(request, http.StatusOK, `{"Version":"`+version+`"}`), nil
		case "/" + modulePath + "/@v/" + version + ".zip":
			return bytesProxyResponse(request, http.StatusOK, archive), nil
		default:
			t.Errorf("unexpected proxy request URL: %s", request.URL)
			return proxyResponse(request, http.StatusBadGateway, "unexpected request"), nil
		}
	})
}

func TestRepositoryAnalysisCompletesAndPersistsResolvedModule(t *testing.T) {
	const (
		requestedModule = "github.com/example/repo"
		resolvedModule  = "github.com/example/resolved"
		version         = "v1.0.0"
	)
	archive := makeModuleZip(t, resolvedModule, version, true)
	setHTTPTransport(t, roundTripFunc(func(request *http.Request) (*http.Response, error) {
		switch request.URL.Path {
		case "/" + requestedModule + "/@latest", "/" + resolvedModule + "/@latest":
			return proxyResponse(request, http.StatusOK, `{"Version":"`+version+`"}`), nil
		case "/" + requestedModule + "/@v/" + version + ".mod":
			return proxyResponse(request, http.StatusOK, "module "+resolvedModule+"\n"), nil
		case "/" + resolvedModule + "/@v/" + version + ".zip":
			return bytesProxyResponse(request, http.StatusOK, archive), nil
		default:
			t.Errorf("unexpected proxy request URL: %s", request.URL)
			return proxyResponse(request, http.StatusBadGateway, "unexpected request"), nil
		}
	}))

	store := &coverageStore{}
	got, err := runRepositoryAnalysis(store, requestedModule, false)
	if err != nil {
		t.Fatalf("runRepositoryAnalysis returned error: %v", err)
	}
	if got.Repo != resolvedModule || got.ResolvedRepo != resolvedModule || got.Version != version {
		t.Errorf("analysis report = (%q, %q, %q), want resolved module %q at %q", got.Repo, got.ResolvedRepo, got.Version, resolvedModule, version)
	}
	if saved, ok := store.testStore.reports[resolvedModule]; !ok || saved.Version != version {
		t.Errorf("saved report = (%+v, %t), want report for resolved module", saved, ok)
	}
	if len(store.testStore.recent) != 1 || store.testStore.recent[0] != resolvedModule {
		t.Errorf("recent repositories = %v, want [%s]", store.testStore.recent, resolvedModule)
	}
}

func TestRepositoryAnalysisHandlesProxyFailuresWithoutNetwork(t *testing.T) {
	t.Run("module lookup fails", func(t *testing.T) {
		setHTTPTransport(t, roundTripFunc(func(request *http.Request) (*http.Response, error) {
			return proxyResponse(request, http.StatusBadGateway, "proxy unavailable"), nil
		}))

		_, err := runRepositoryAnalysis(&coverageStore{}, "github.com/example/repo", false)
		if err == nil || !strings.Contains(err.Error(), "could not analyze the repository") || !strings.Contains(err.Error(), "could not download repo") {
			t.Fatalf("runRepositoryAnalysis error = %v, want wrapped proxy download error", err)
		}
	})

	t.Run("resolved module name is used", func(t *testing.T) {
		var originalLatestRequests int
		var resolvedLatestRequested bool
		setHTTPTransport(t, roundTripFunc(func(request *http.Request) (*http.Response, error) {
			switch request.URL.Path {
			case "/github.com/example/repo/@latest":
				originalLatestRequests++
				if originalLatestRequests == 1 {
					return proxyResponse(request, http.StatusOK, `{"Version":"v1.0.0"}`), nil
				}
				return proxyResponse(request, http.StatusBadGateway, "download unavailable"), nil
			case "/github.com/example/repo/@v/v1.0.0.mod":
				return proxyResponse(request, http.StatusOK, "module github.com/example/resolved\n"), nil
			case "/github.com/example/resolved/@latest":
				resolvedLatestRequested = true
				return proxyResponse(request, http.StatusBadGateway, "download unavailable"), nil
			default:
				t.Errorf("unexpected proxy request URL: %s", request.URL)
				return proxyResponse(request, http.StatusBadGateway, "unexpected request"), nil
			}
		}))

		_, err := runRepositoryAnalysis(&coverageStore{}, "github.com/example/repo", false)
		if err == nil || !strings.Contains(err.Error(), "could not download repo") {
			t.Fatalf("runRepositoryAnalysis error = %v, want download error", err)
		}
		if !resolvedLatestRequested {
			t.Error("analysis did not use the module name found in go.mod")
		}
	})
}

func TestNewChecksRespHandlesStorageWriteResults(t *testing.T) {
	const (
		repo    = "github.com/example/storage-outcomes"
		version = "v1.0.0"
	)
	archive := makeModuleZip(t, repo, version, true)

	t.Run("save failure is returned", func(t *testing.T) {
		setHTTPTransport(t, moduleProxyTransport(t, repo, version, archive))
		saveErr := errors.New("database write failed")
		_, err := newChecksResp(context.Background(), &coverageStore{saveErr: saveErr}, repo, false)
		if !errors.Is(err, saveErr) || !strings.Contains(err.Error(), "could not save report") {
			t.Fatalf("save error = %v, want wrapped storage error", err)
		}
	})

	t.Run("recent update failure is non-fatal", func(t *testing.T) {
		setHTTPTransport(t, moduleProxyTransport(t, repo, version, archive))
		store := &coverageStore{addRecentErr: errors.New("recent update failed")}
		got, err := newChecksResp(context.Background(), store, repo, false)
		if err != nil {
			t.Fatalf("recent update error should not fail analysis: %v", err)
		}
		if got.Repo != repo || store.testStore.reports[repo].Repo != repo {
			t.Errorf("analysis report was not saved: report=%+v, saved=%+v", got, store.testStore.reports[repo])
		}
	})
}

func TestNewChecksRespLogsDownloadedModuleCleanupFailure(t *testing.T) {
	const (
		repo    = "github.com/example/read-only-module"
		version = "v1.0.0"
	)
	moduleDir := filepath.Join("_repos", "src", repo+"@"+version)
	readOnlyDir := filepath.Join(moduleDir, "readonly")
	t.Cleanup(func() {
		_ = os.Chmod(readOnlyDir, 0o755)
		_ = os.Chmod(moduleDir, 0o755)
		_ = os.RemoveAll(moduleDir)
	})
	archive := makeModuleZipWithReadOnlyDirectory(t, repo, version)
	setHTTPTransport(t, moduleProxyTransport(t, repo, version, archive))

	_, err := newChecksResp(context.Background(), &coverageStore{}, repo, true)
	if err == nil || !strings.Contains(err.Error(), "no .go files found") {
		t.Fatalf("newChecksResp error = %v, want no-Go-files check error", err)
	}
	if _, err := os.Stat(readOnlyDir); err != nil {
		t.Fatalf("read-only directory was removed; expected deferred cleanup to report a failure: %v", err)
	}
}

func TestNewChecksRespReturnsCheckFailureAndCleansDownloadedModule(t *testing.T) {
	const (
		repo    = "github.com/example/no-go-files"
		version = "v1.0.0"
	)
	archive := makeModuleZip(t, repo, version, false)
	setHTTPTransport(t, moduleProxyTransport(t, repo, version, archive))

	_, err := newChecksResp(context.Background(), &coverageStore{}, repo, true)
	if err == nil || !strings.Contains(err.Error(), "no .go files found") {
		t.Fatalf("newChecksResp error = %v, want no-Go-files check error", err)
	}
	moduleDir := filepath.Join("_repos", "src", repo+"@"+version)
	if _, err := os.Stat(moduleDir); !os.IsNotExist(err) {
		t.Errorf("downloaded module still exists after check failure: stat error = %v", err)
	}
}

func TestNewChecksRespForcedRefreshSkipsCache(t *testing.T) {
	setHTTPTransport(t, roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return proxyResponse(request, http.StatusBadGateway, "proxy unavailable"), nil
	}))
	storeErr := errors.New("cache should not be read for forced refresh")
	_, err := newChecksResp(context.Background(), &coverageStore{getErr: storeErr}, "github.com/example/repo", true)
	if err == nil || !strings.Contains(err.Error(), "could not download repo") {
		t.Fatalf("forced refresh error = %v, want proxy download error", err)
	}
}

func TestHandleChecksDelegatesAnalysisToRepositoryRunner(t *testing.T) {
	const repo = "github.com/example/handle-checks-wrapper"
	setHTTPTransport(t, roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return proxyResponse(request, http.StatusBadGateway, "proxy unavailable"), nil
	}))

	response := httptest.NewRecorder()
	handleChecks(response, httptest.NewRequest(http.MethodPost, "/checks", nil), &coverageStore{}, repo)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"redirect":"/report/`+repo+`?wait=1"`) {
		t.Fatalf("POST response = (%d, %s), want analysis redirect", response.Code, response.Body.String())
	}
	job := waitForAnalysisJob(t, repositoryAnalysisJobs, repo)
	if job.err == nil || !strings.Contains(job.err.Error(), "could not analyze the repository") {
		t.Fatalf("analysis job error = %v, want repository analysis failure", job.err)
	}
}

func TestWriteAnalysisJobStatusReturnsUnavailableForMissingJob(t *testing.T) {
	response := httptest.NewRecorder()
	writeAnalysisJobStatus(response, newAnalysisManager(1, 1), "github.com/example/disappeared")

	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusServiceUnavailable)
	}
	if got, want := response.Body.String(), "analysis job is no longer available; retry the request\n"; got != want {
		t.Errorf("body = %q, want %q", got, want)
	}
	if got, want := response.Header().Get("Content-Type"), "text/plain; charset=utf-8"; got != want {
		t.Errorf("Content-Type = %q, want %q", got, want)
	}
}

func TestAnalysisManagerHandlesPanicsAndMissingStatus(t *testing.T) {
	manager := newAnalysisManager(0, 0)
	if cap(manager.slots) != 1 || manager.maxActive != 1 {
		t.Fatalf("manager limits = (%d, %d), want (1, 1)", cap(manager.slots), manager.maxActive)
	}
	if _, ok := manager.status("unknown"); ok {
		t.Fatal("status found a job that was never started")
	}

	if err := manager.start("github.com/example/panic", true, func() (checksResp, error) {
		panic("test panic")
	}); err != nil {
		t.Fatal(err)
	}
	job := waitForAnalysisJob(t, manager, "github.com/example/panic")
	if job.err == nil || !strings.Contains(job.err.Error(), "analysis failed unexpectedly: test panic") {
		t.Fatalf("panic job error = %v, want recovered analysis error", job.err)
	}

	var rerun bool
	if err := manager.start("github.com/example/panic", false, func() (checksResp, error) {
		rerun = true
		return checksResp{}, nil
	}); err != nil {
		t.Fatal(err)
	}
	if rerun {
		t.Error("completed job restarted when restartCompleted was false")
	}
}

func TestAnalysisManagerPrunesExpiredAndOldestJobs(t *testing.T) {
	now := time.Now()
	manager := newAnalysisManager(1, maxAnalysisHistory)
	manager.jobs["expired"] = &analysisJob{done: true, completedAt: now.Add(-analysisJobTTL - time.Second)}
	manager.jobs["recent"] = &analysisJob{done: true, completedAt: now}

	manager.mu.Lock()
	manager.pruneLocked(now)
	manager.mu.Unlock()
	if _, ok := manager.jobs["expired"]; ok {
		t.Error("expired completed job was retained")
	}
	if _, ok := manager.jobs["recent"]; !ok {
		t.Error("recent completed job was removed")
	}

	manager.jobs = make(map[string]*analysisJob, maxAnalysisHistory)
	for i := 0; i < maxAnalysisHistory; i++ {
		manager.jobs[strings.Repeat("x", i+1)] = &analysisJob{
			done:        true,
			completedAt: now.Add(-time.Duration(i) * time.Second),
		}
	}
	manager.mu.Lock()
	manager.pruneLocked(now)
	manager.mu.Unlock()
	if len(manager.jobs) != maxAnalysisHistory-1 {
		t.Fatalf("history size after pruning = %d, want %d", len(manager.jobs), maxAnalysisHistory-1)
	}
}

func TestAnalysisManagerDoesNotPruneInFlightJobs(t *testing.T) {
	manager := newAnalysisManager(1, maxAnalysisHistory)
	for i := 0; i < maxAnalysisHistory; i++ {
		manager.jobs[strings.Repeat("y", i+1)] = &analysisJob{}
	}
	manager.mu.Lock()
	manager.pruneLocked(time.Now())
	manager.mu.Unlock()
	if len(manager.jobs) != maxAnalysisHistory {
		t.Fatalf("in-flight history size = %d, want %d", len(manager.jobs), maxAnalysisHistory)
	}
}

func TestHandleChecksMethodCacheErrorAndCompletedFailure(t *testing.T) {
	const repo = "github.com/example/repo"
	analyze := func(string, bool) (checksResp, error) {
		t.Error("analyze called for a request that should not start work")
		return checksResp{}, nil
	}

	t.Run("unsupported method", func(t *testing.T) {
		response := httptest.NewRecorder()
		handleChecksWithManager(response, httptest.NewRequest(http.MethodPut, "/checks", nil), &coverageStore{}, repo, newAnalysisManager(1, 1), analyze)
		if response.Code != http.StatusMethodNotAllowed {
			t.Fatalf("status = %d, want %d", response.Code, http.StatusMethodNotAllowed)
		}
	})

	t.Run("cached report", func(t *testing.T) {
		store := &coverageStore{testStore: testStore{reports: map[string]storage.Report{repo: {Repo: repo}}}}
		response := httptest.NewRecorder()
		handleChecksWithManager(response, httptest.NewRequest(http.MethodGet, "/checks", nil), store, repo, newAnalysisManager(1, 1), analyze)
		if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"redirect":"/report/`+repo+`"`) {
			t.Fatalf("cached response = (%d, %s), want report redirect", response.Code, response.Body.String())
		}
	})

	t.Run("cache read error", func(t *testing.T) {
		response := httptest.NewRecorder()
		handleChecksWithManager(response, httptest.NewRequest(http.MethodGet, "/checks", nil), &coverageStore{getErr: errors.New("database offline")}, repo, newAnalysisManager(1, 1), analyze)
		if response.Code != http.StatusInternalServerError || !strings.Contains(response.Body.String(), "Could not read report cache") {
			t.Fatalf("cache error response = (%d, %s), want HTTP 500 cache error", response.Code, response.Body.String())
		}
	})

	t.Run("completed analysis error", func(t *testing.T) {
		manager := newAnalysisManager(1, 1)
		manager.jobs[repo] = &analysisJob{done: true, err: errors.New("analysis failed"), completedAt: time.Now()}
		response := httptest.NewRecorder()
		handleChecksWithManager(response, httptest.NewRequest(http.MethodGet, "/checks?refresh=1", nil), &coverageStore{}, repo, manager, analyze)
		if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "analysis failed") {
			t.Fatalf("completed error response = (%d, %s), want HTTP 400 analysis failure", response.Code, response.Body.String())
		}
	})

	t.Run("queue full", func(t *testing.T) {
		manager := newAnalysisManager(1, 1)
		manager.active = manager.maxActive
		response := httptest.NewRecorder()
		handleChecksWithManager(response, httptest.NewRequest(http.MethodPost, "/checks", nil), &coverageStore{}, repo, manager, analyze)
		if response.Code != http.StatusServiceUnavailable || !strings.Contains(response.Body.String(), "too many repository analyses") {
			t.Fatalf("queue-full response = (%d, %s), want HTTP 503", response.Code, response.Body.String())
		}
	})
}

type failingResponseWriter struct {
	header http.Header
	status int
}

func (w *failingResponseWriter) Header() http.Header {
	if w.header == nil {
		w.header = make(http.Header)
	}
	return w.header
}

func (w *failingResponseWriter) WriteHeader(status int) { w.status = status }
func (w *failingResponseWriter) Write([]byte) (int, error) {
	return 0, errors.New("response writer closed")
}

func TestWriteCheckJobResponseHandlesWriteFailure(t *testing.T) {
	writer := &failingResponseWriter{}
	writeCheckJobResponse(writer, http.StatusAccepted, checkJobResponse{Pending: true})
	if writer.status != http.StatusAccepted {
		t.Errorf("status = %d, want %d", writer.status, http.StatusAccepted)
	}
	if got := writer.Header().Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", got)
	}
}

func TestCheckHandlerAcceptsCachedRepositoryAndRejectsEmptyInput(t *testing.T) {
	const repo = "github.com/example/repo"
	store := &coverageStore{testStore: testStore{reports: map[string]storage.Report{repo: {Repo: repo}}}}
	request := httptest.NewRequest(http.MethodGet, "/checks?repo=https%3A%2F%2F"+repo, nil)
	response := httptest.NewRecorder()
	CheckHandler(response, request, store)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "/report/"+repo) {
		t.Fatalf("valid repo response = (%d, %s), want cached report redirect", response.Code, response.Body.String())
	}

	emptyResponse := httptest.NewRecorder()
	CheckHandler(emptyResponse, httptest.NewRequest(http.MethodPost, "/checks", nil), store)
	if emptyResponse.Code != http.StatusBadRequest || !strings.Contains(emptyResponse.Body.String(), "please enter a repository") {
		t.Fatalf("empty repo response = (%d, %s), want HTTP 400 validation error", emptyResponse.Code, emptyResponse.Body.String())
	}
}

func TestBadgeHandlerUsesCachedGradeAndFallsBackOnErrors(t *testing.T) {
	const repo = "github.com/example/repo"
	t.Run("cached grade", func(t *testing.T) {
		store := &coverageStore{testStore: testStore{reports: map[string]storage.Report{
			repo: {Repo: repo, Grade: check.GradeA},
		}}}
		response := httptest.NewRecorder()
		BadgeHandler(response, httptest.NewRequest(http.MethodGet, "/badge/"+repo+"?style=flat-square", nil), store, repo)
		if response.Code != http.StatusTemporaryRedirect {
			t.Fatalf("status = %d, want %d", response.Code, http.StatusTemporaryRedirect)
		}
		if got, want := response.Header().Get("Location"), "https://img.shields.io/badge/go%20report-A-green.svg?style=flat-square"; got != want {
			t.Errorf("Location = %q, want %q", got, want)
		}
	})

	t.Run("cache error", func(t *testing.T) {
		response := httptest.NewRecorder()
		BadgeHandler(response, httptest.NewRequest(http.MethodGet, "/badge/"+repo+"?style=plastic", nil), &coverageStore{getErr: errors.New("database offline")}, repo)
		if got, want := response.Header().Get("Location"), "https://img.shields.io/badge/go%20report-error-lightgrey.svg?style=plastic"; got != want {
			t.Errorf("fallback Location = %q, want %q", got, want)
		}
	})

	t.Run("report with analysis errors", func(t *testing.T) {
		store := &coverageStore{testStore: testStore{reports: map[string]storage.Report{
			repo: {Repo: repo, Grade: check.GradeB, DidError: true},
		}}}
		response := httptest.NewRecorder()
		BadgeHandler(response, httptest.NewRequest(http.MethodGet, "/badge/"+repo, nil), store, repo)
		if got, want := response.Header().Get("Location"), "https://img.shields.io/badge/go%20report-error-lightgrey.svg?style=flat"; got != want {
			t.Errorf("analysis-error Location = %q, want %q", got, want)
		}
	})
}

func TestReportHandlerRendersCachedReportAndContinuesOnCacheError(t *testing.T) {
	const repo = "github.com/example/repo"
	t.Run("cached report", func(t *testing.T) {
		store := &coverageStore{testStore: testStore{reports: map[string]storage.Report{
			repo: {Repo: repo, Average: 0.8, Grade: check.GradeA, Files: 4},
		}}}
		response := httptest.NewRecorder()
		(&GRCHandler{AssetsFS: http.Dir("../assets")}).ReportHandler(response, httptest.NewRequest(http.MethodGet, "/report/"+repo, nil), store, repo)
		if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"repo":"`+repo+`"`) {
			t.Fatalf("cached report response = (%d, %s), want rendered cached report", response.Code, response.Body.String())
		}
		if !strings.Contains(response.Body.String(), "var loading =  false ") {
			t.Error("cached report incorrectly renders in the loading state")
		}
	})

	t.Run("store failure shows loading page", func(t *testing.T) {
		response := httptest.NewRecorder()
		(&GRCHandler{AssetsFS: http.Dir("../assets")}).ReportHandler(response, httptest.NewRequest(http.MethodGet, "/report/"+repo, nil), &coverageStore{getErr: errors.New("database offline")}, repo)
		if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "var loading =  true ") {
			t.Fatalf("store failure response = (%d, %s), want loading page", response.Code, response.Body.String())
		}
	})
}

func TestHighScoresHandlerRendersScoresAndHandlesFailures(t *testing.T) {
	t.Run("renders scores", func(t *testing.T) {
		store := &coverageStore{testStore: testStore{
			count:  1234,
			scores: []storage.HighScore{{Repo: "github.com/example/top", Score: 0.987, Files: 8}},
		}}
		response := httptest.NewRecorder()
		(&GRCHandler{AssetsFS: http.Dir("../assets")}).HighScoresHandler(response, httptest.NewRequest(http.MethodGet, "/high_scores", nil), store)
		if response.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d: %s", response.Code, http.StatusOK, response.Body.String())
		}
		for _, want := range []string{"github.com/example/top", "8", "0.99", "1,234"} {
			if !strings.Contains(response.Body.String(), want) {
				t.Errorf("high-scores page does not contain %q", want)
			}
		}
	})

	t.Run("storage failure", func(t *testing.T) {
		response := httptest.NewRecorder()
		(&GRCHandler{AssetsFS: http.Dir("../assets")}).HighScoresHandler(response, httptest.NewRequest(http.MethodGet, "/high_scores", nil), &coverageStore{highScoresErr: errors.New("database offline")})
		if response.Code != http.StatusInternalServerError || !strings.Contains(response.Body.String(), "database offline") {
			t.Fatalf("storage failure response = (%d, %s), want HTTP 500", response.Code, response.Body.String())
		}
	})

	t.Run("template failure", func(t *testing.T) {
		response := httptest.NewRecorder()
		(&GRCHandler{AssetsFS: http.FS(fstest.MapFS{})}).HighScoresHandler(response, httptest.NewRequest(http.MethodGet, "/high_scores", nil), &coverageStore{})
		if response.Code != http.StatusInternalServerError {
			t.Fatalf("template failure status = %d, want %d", response.Code, http.StatusInternalServerError)
		}
	})
}

func TestHomeHandlerReportsTemplateLoadAndExecutionErrors(t *testing.T) {
	handler := &GRCHandler{AssetsFS: http.FS(fstest.MapFS{})}
	response := httptest.NewRecorder()
	handler.HomeHandler(response, httptest.NewRequest(http.MethodGet, "/", nil), &coverageStore{})
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("template load status = %d, want %d", response.Code, http.StatusInternalServerError)
	}

	assets := mapFileSystem(map[string]string{
		"templates/home.html": `[[ define "content" ]]home[[ end ]]`,
		"templates/base.html": `[[ define "base" ]][[ template "content" . ]][[ add "wrong type" 1 ]][[ end ]]`,
	})
	response = httptest.NewRecorder()
	(&GRCHandler{AssetsFS: assets}).HomeHandler(response, httptest.NewRequest(http.MethodGet, "/", nil), &coverageStore{})
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "home") {
		t.Fatalf("template execution response = (%d, %s), want partial homepage output", response.Code, response.Body.String())
	}
}

func TestHomeHandlerUsesRecentCacheAndSurvivesRecentStoreErrors(t *testing.T) {
	oldItems, oldCount := cache.items, cache.count
	t.Cleanup(func() {
		cache.mux.Lock()
		cache.items, cache.count = oldItems, oldCount
		cache.mux.Unlock()
	})

	cache.mux.Lock()
	cache.items = []string{"github.com/example/one", "github.com/example/two", "github.com/example/three", "github.com/example/four", "github.com/example/five"}
	cache.count = 0
	cache.mux.Unlock()

	response := httptest.NewRecorder()
	(&GRCHandler{AssetsFS: http.Dir("../assets")}).HomeHandler(response, httptest.NewRequest(http.MethodGet, "/", nil), &coverageStore{})
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "github.com/example/five") {
		t.Fatalf("cached home response = (%d, %s), want cached recent repositories", response.Code, response.Body.String())
	}

	cache.mux.Lock()
	cache.items = nil
	cache.count = 0
	cache.mux.Unlock()
	response = httptest.NewRecorder()
	(&GRCHandler{AssetsFS: http.Dir("../assets")}).HomeHandler(response, httptest.NewRequest(http.MethodGet, "/", nil), &coverageStore{recentErr: errors.New("database offline")})
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "Recently Generated") {
		t.Fatalf("recent store error response = (%d, %s), want rendered homepage", response.Code, response.Body.String())
	}
}

func TestAboutAndSunsetHandlersRenderAndReportTemplateErrors(t *testing.T) {
	assets := http.Dir("../assets")
	for name, handler := range map[string]func(http.ResponseWriter, *http.Request){
		"about":  (&GRCHandler{AssetsFS: assets}).AboutHandler,
		"sunset": (&GRCHandler{AssetsFS: assets}).SunsetHandler,
	} {
		t.Run(name+" renders", func(t *testing.T) {
			response := httptest.NewRecorder()
			handler(response, httptest.NewRequest(http.MethodGet, "/"+name, nil))
			if response.Code != http.StatusOK || response.Body.Len() == 0 {
				t.Fatalf("%s response = (%d, %s), want rendered page", name, response.Code, response.Body.String())
			}
		})
	}

	executionErrorAssets := mapFileSystem(map[string]string{
		"templates/about.html":  `[[ define "content" ]]about[[ end ]]`,
		"templates/sunset.html": `[[ define "content" ]]sunset[[ end ]]`,
		"templates/base.html":   `[[ define "base" ]][[ template "content" . ]][[ add "wrong type" 1 ]][[ end ]]`,
	})
	for name, handler := range map[string]func(http.ResponseWriter, *http.Request){
		"about":  (&GRCHandler{AssetsFS: executionErrorAssets}).AboutHandler,
		"sunset": (&GRCHandler{AssetsFS: executionErrorAssets}).SunsetHandler,
	} {
		t.Run(name+" template execution error", func(t *testing.T) {
			response := httptest.NewRecorder()
			handler(response, httptest.NewRequest(http.MethodGet, "/"+name, nil))
			if response.Code != http.StatusOK || response.Body.Len() == 0 {
				t.Fatalf("%s execution-error response = (%d, %s), want partial page output", name, response.Code, response.Body.String())
			}
		})
	}

	badAssets := http.FS(fstest.MapFS{})
	for name, handler := range map[string]func(http.ResponseWriter, *http.Request){
		"about":  (&GRCHandler{AssetsFS: badAssets}).AboutHandler,
		"sunset": (&GRCHandler{AssetsFS: badAssets}).SunsetHandler,
	} {
		t.Run(name+" template missing", func(t *testing.T) {
			response := httptest.NewRecorder()
			handler(response, httptest.NewRequest(http.MethodGet, "/"+name, nil))
			if response.Code != http.StatusInternalServerError {
				t.Fatalf("%s template error status = %d, want %d", name, response.Code, http.StatusInternalServerError)
			}
		})
	}
}

func TestErrorHandlerHandlesMissingAndInvalid404Templates(t *testing.T) {
	missing := httptest.NewRecorder()
	(&GRCHandler{AssetsFS: http.FS(fstest.MapFS{})}).errorHandler(missing, httptest.NewRequest(http.MethodGet, "/missing", nil), http.StatusNotFound)
	if missing.Code != http.StatusNotFound || !strings.Contains(missing.Body.String(), "404.html") {
		t.Fatalf("missing 404 template response = (%d, %s), want not-found template error", missing.Code, missing.Body.String())
	}

	assets := mapFileSystem(map[string]string{
		"templates/404.html":  `[[ define "content" ]]not found[[ end ]]`,
		"templates/base.html": `[[ define "base" ]][[ template "content" . ]][[ add "wrong type" 1 ]][[ end ]]`,
	})
	invalid := httptest.NewRecorder()
	(&GRCHandler{AssetsFS: assets}).errorHandler(invalid, httptest.NewRequest(http.MethodGet, "/missing", nil), http.StatusNotFound)
	if invalid.Code != http.StatusNotFound || !strings.Contains(invalid.Body.String(), "not found") {
		t.Fatalf("invalid 404 template response = (%d, %s), want partial 404 page", invalid.Code, invalid.Body.String())
	}
}

func TestHomeHandlerReturnsNotFoundForUnknownPath(t *testing.T) {
	response := httptest.NewRecorder()
	(&GRCHandler{AssetsFS: http.Dir("../assets")}).HomeHandler(response, httptest.NewRequest(http.MethodGet, "/unknown", nil), &coverageStore{})
	if response.Code != http.StatusNotFound || !strings.Contains(response.Body.String(), "Page not found!") {
		t.Fatalf("unknown path response = (%d, %s), want rendered 404 page", response.Code, response.Body.String())
	}

	nonNotFound := httptest.NewRecorder()
	(&GRCHandler{AssetsFS: http.Dir("../assets")}).errorHandler(nonNotFound, httptest.NewRequest(http.MethodGet, "/", nil), http.StatusForbidden)
	if nonNotFound.Code != http.StatusForbidden {
		t.Errorf("non-404 error status = %d, want %d", nonNotFound.Code, http.StatusForbidden)
	}
}

type readErrorFile struct {
	http.File
	err error
}

func (f readErrorFile) Read([]byte) (int, error) { return 0, f.err }

type testFileSystem struct {
	base          http.FileSystem
	openErrorName string
	openErr       error
	readErrorName string
	readErr       error
}

func (fs testFileSystem) Open(name string) (http.File, error) {
	if name == fs.openErrorName && fs.openErr != nil {
		return nil, fs.openErr
	}
	file, err := fs.base.Open(name)
	if err != nil {
		return nil, err
	}
	if name == fs.readErrorName {
		return readErrorFile{File: file, err: fs.readErr}, nil
	}
	return file, nil
}

func mapFileSystem(contents map[string]string) http.FileSystem {
	files := make(fstest.MapFS, len(contents))
	for name, content := range contents {
		files[name] = &fstest.MapFile{Data: []byte(content)}
	}
	return http.FS(files)
}

func TestLoadTemplateReturnsOpenReadAndParseErrors(t *testing.T) {
	openErr := errors.New("asset open failed")
	readErr := errors.New("asset read failed")
	valid := map[string]string{
		"templates/report.html": `[[ define "content" ]]report[[ end ]]`,
		"templates/about.html":  `[[ define "content" ]]about[[ end ]]`,
		"templates/base.html":   `[[ define "base" ]][[ template "content" . ]][[ end ]]`,
	}

	tests := []struct {
		name string
		fs   http.FileSystem
		path string
	}{
		{
			name: "requested asset open error",
			fs: testFileSystem{
				base:          mapFileSystem(valid),
				openErrorName: "/templates/report.html",
				openErr:       openErr,
			},
			path: "/templates/report.html",
		},
		{
			name: "requested asset read error",
			fs: testFileSystem{
				base:          mapFileSystem(valid),
				readErrorName: "/templates/report.html",
				readErr:       readErr,
			},
			path: "/templates/report.html",
		},
		{
			name: "template parse error",
			fs: mapFileSystem(map[string]string{
				"templates/report.html": `[[ if ]]`,
			}),
			path: "/templates/report.html",
		},
		{
			name: "base asset open error",
			fs: mapFileSystem(map[string]string{
				"templates/about.html": valid["templates/about.html"],
			}),
			path: "/templates/about.html",
		},
		{
			name: "base asset read error",
			fs: testFileSystem{
				base:          mapFileSystem(valid),
				readErrorName: "/templates/base.html",
				readErr:       readErr,
			},
			path: "/templates/about.html",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := (&GRCHandler{AssetsFS: test.fs}).loadTemplate(test.path)
			if err == nil {
				t.Fatal("loadTemplate returned no error")
			}
		})
	}
}

func TestScoreHeapOrdersItemsByScore(t *testing.T) {
	h := &ScoreHeap{}
	heap.Init(h)
	heap.Push(h, scoreItem{Repo: "high", Score: 0.9})
	heap.Push(h, scoreItem{Repo: "low", Score: 0.2})
	heap.Push(h, scoreItem{Repo: "middle", Score: 0.5})
	if h.Len() != 3 {
		t.Fatalf("heap length = %d, want 3", h.Len())
	}
	if (*h)[0].Repo != "low" || !(*h).Less(0, 1) {
		t.Error("heap root and Less do not reflect score ordering")
	}
	(*h).Swap(0, 1)
	(*h).Swap(0, 1)
	for _, want := range []string{"low", "middle", "high"} {
		got := heap.Pop(h).(scoreItem).Repo
		if got != want {
			t.Fatalf("popped repo = %q, want %q", got, want)
		}
	}
	if h.Len() != 0 {
		t.Errorf("heap length after pops = %d, want 0", h.Len())
	}
}

func TestReportHandlerHandlesTemplateLoadAndExecutionErrors(t *testing.T) {
	const repo = "github.com/example/repo"
	response := httptest.NewRecorder()
	(&GRCHandler{AssetsFS: http.FS(fstest.MapFS{})}).ReportHandler(response, httptest.NewRequest(http.MethodGet, "/report/"+repo, nil), &coverageStore{}, repo)
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("missing report template status = %d, want %d", response.Code, http.StatusInternalServerError)
	}

	assets := mapFileSystem(map[string]string{
		"templates/report.html": `[[ add "wrong type" 1 ]]`,
	})
	response = httptest.NewRecorder()
	(&GRCHandler{AssetsFS: assets}).ReportHandler(response, httptest.NewRequest(http.MethodGet, "/report/"+repo, nil), &coverageStore{}, repo)
	if response.Code != http.StatusOK {
		t.Fatalf("report execution-error status = %d, want %d", response.Code, http.StatusOK)
	}
}

func TestReportHandlerReturnsErrorForUnmarshalableCacheTime(t *testing.T) {
	const repo = "github.com/example/repo"
	store := &coverageStore{testStore: testStore{reports: map[string]storage.Report{
		repo: {Repo: repo, LastRefresh: time.Date(10000, time.January, 1, 0, 0, 0, 0, time.UTC)},
	}}}
	response := httptest.NewRecorder()
	(&GRCHandler{AssetsFS: http.Dir("../assets")}).ReportHandler(response, httptest.NewRequest(http.MethodGet, "/report/"+repo, nil), store, repo)
	if response.Code != http.StatusInternalServerError || !strings.Contains(response.Body.String(), "Failed to load cache object") {
		t.Fatalf("unmarshalable cache response = (%d, %s), want HTTP 500", response.Code, response.Body.String())
	}
}

func TestHighScoresHandlerLogsTemplateExecutionErrors(t *testing.T) {
	assets := mapFileSystem(map[string]string{
		"templates/high_scores.html": `[[ define "content" ]][[ formatScore "wrong type" ]][[ end ]]`,
		"templates/base.html":        `[[ define "base" ]][[ template "content" . ]][[ end ]]`,
	})
	response := httptest.NewRecorder()
	(&GRCHandler{AssetsFS: assets}).HighScoresHandler(response, httptest.NewRequest(http.MethodGet, "/high_scores", nil), &coverageStore{})
	if response.Code != http.StatusOK {
		t.Fatalf("template execution-error status = %d, want %d", response.Code, http.StatusOK)
	}
}

func TestLoadTemplateReportDoesNotRequireBaseTemplate(t *testing.T) {
	assets := mapFileSystem(map[string]string{
		"templates/report.html": `[[ .repo ]]`,
	})
	template, err := (&GRCHandler{AssetsFS: assets}).loadTemplate("/templates/report.html")
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := template.Execute(&output, map[string]string{"repo": "github.com/example/repo"}); err != nil {
		t.Fatal(err)
	}
	if got, want := output.String(), "github.com/example/repo"; got != want {
		t.Errorf("rendered report template = %q, want %q", got, want)
	}
}
