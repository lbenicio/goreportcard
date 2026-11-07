package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func waitForAnalysisJob(t *testing.T, manager *analysisManager, repo string) analysisJobSnapshot {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		job, ok := manager.status(repo)
		if ok && job.done {
			return job
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("analysis job %q did not complete", repo)
	return analysisJobSnapshot{}
}

func TestAnalysisManagerDeduplicatesInFlightJobs(t *testing.T) {
	manager := newAnalysisManager(1, 2)
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	var calls atomic.Int32
	run := func() (checksResp, error) {
		calls.Add(1)
		started <- struct{}{}
		<-release
		return checksResp{Repo: "github.com/example/repo"}, nil
	}

	if err := manager.start("github.com/example/repo", true, run); err != nil {
		t.Fatal(err)
	}
	<-started
	if err := manager.start("github.com/example/repo", true, func() (checksResp, error) {
		calls.Add(1)
		return checksResp{}, nil
	}); err != nil {
		t.Fatal(err)
	}

	job, ok := manager.status("github.com/example/repo")
	if !ok || job.done {
		t.Fatalf("status = (%+v, %t), want an in-flight job", job, ok)
	}
	close(release)
	job = waitForAnalysisJob(t, manager, "github.com/example/repo")
	if calls.Load() != 1 {
		t.Errorf("runner calls = %d, want 1", calls.Load())
	}
	if job.err != nil || job.report.Repo != "github.com/example/repo" {
		t.Errorf("completed job = (%+v, %v), want successful report", job.report, job.err)
	}
}

func TestAnalysisManagerCanRestartCompletedJob(t *testing.T) {
	manager := newAnalysisManager(1, 2)
	var calls atomic.Int32
	run := func() (checksResp, error) {
		return checksResp{Repo: "github.com/example/repo", Files: int(calls.Add(1))}, nil
	}

	if err := manager.start("github.com/example/repo", true, run); err != nil {
		t.Fatal(err)
	}
	first := waitForAnalysisJob(t, manager, "github.com/example/repo")
	if first.report.Files != 1 {
		t.Fatalf("first report files = %d, want 1", first.report.Files)
	}

	if err := manager.start("github.com/example/repo", true, run); err != nil {
		t.Fatal(err)
	}
	second := waitForAnalysisJob(t, manager, "github.com/example/repo")
	if calls.Load() != 2 || second.report.Files != 2 {
		t.Errorf("second run calls/files = %d/%d, want 2/2", calls.Load(), second.report.Files)
	}
}

func TestAnalysisManagerBoundsQueuedJobs(t *testing.T) {
	manager := newAnalysisManager(1, 1)
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	if err := manager.start("github.com/example/first", true, func() (checksResp, error) {
		started <- struct{}{}
		<-release
		return checksResp{}, nil
	}); err != nil {
		t.Fatal(err)
	}
	<-started

	err := manager.start("github.com/example/second", true, func() (checksResp, error) {
		return checksResp{}, nil
	})
	if err != errAnalysisQueueFull {
		t.Fatalf("second start error = %v, want %v", err, errAnalysisQueueFull)
	}
	close(release)
	waitForAnalysisJob(t, manager, "github.com/example/first")
}

func TestHandleChecksPollsAsyncJob(t *testing.T) {
	const repo = "github.com/example/repo"
	manager := newAnalysisManager(1, 2)
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	released := false
	defer func() {
		if !released {
			close(release)
		}
	}()
	analyze := func(string, bool) (checksResp, error) {
		started <- struct{}{}
		<-release
		return checksResp{Repo: repo, Files: 3}, nil
	}

	postResponse := httptest.NewRecorder()
	handleChecksWithManager(postResponse, httptest.NewRequest(http.MethodPost, "/checks", nil), nil, repo, manager, analyze)
	if postResponse.Code != http.StatusOK {
		t.Fatalf("POST status = %d, want %d", postResponse.Code, http.StatusOK)
	}
	var postResult checkJobResponse
	if err := json.Unmarshal(postResponse.Body.Bytes(), &postResult); err != nil {
		t.Fatal(err)
	}
	if postResult.Redirect != "/report/"+repo+"?wait=1" {
		t.Fatalf("POST redirect = %q, want report loading URL", postResult.Redirect)
	}
	<-started

	poll := func() (*httptest.ResponseRecorder, checkJobResponse) {
		response := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodGet, "/checks?refresh=1", nil)
		handleChecksWithManager(response, request, nil, repo, manager, analyze)
		var result checkJobResponse
		if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		return response, result
	}

	pendingResponse, pending := poll()
	if pendingResponse.Code != http.StatusAccepted || !pending.Pending {
		t.Fatalf("pending response = (%d, %+v), want HTTP 202 pending", pendingResponse.Code, pending)
	}
	close(release)
	released = true
	waitForAnalysisJob(t, manager, repo)

	completeResponse, complete := poll()
	if completeResponse.Code != http.StatusOK || complete.Report == nil {
		t.Fatalf("complete response = (%d, %+v), want report", completeResponse.Code, complete)
	}
	if complete.Report.Repo != repo || complete.Report.Files != 3 {
		t.Errorf("completed report = %+v, want repo %q with 3 files", complete.Report, repo)
	}
}

func TestCheckHandlerRejectsInvalidRepositoryPath(t *testing.T) {
	form := url.Values{"repo": {"../../outside"}}
	request := httptest.NewRequest(http.MethodPost, "/checks", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response := httptest.NewRecorder()

	CheckHandler(response, request, nil)

	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusBadRequest)
	}
	if !strings.Contains(response.Body.String(), "invalid module path") {
		t.Errorf("response = %q, want invalid module path error", response.Body.String())
	}
}
