package handlers

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"

	badger "github.com/dgraph-io/badger/v2"
	"github.com/gojp/goreportcard/download"
	"golang.org/x/mod/module"
)

const (
	analysisConcurrency = 1
	maxActiveAnalyses   = 16
	maxAnalysisHistory  = 256
	analysisJobTTL      = 30 * time.Minute
)

var errAnalysisQueueFull = errors.New("server is processing too many repository analyses; try again shortly")

type analysisJob struct {
	report      checksResp
	err         error
	done        bool
	completedAt time.Time
}

type analysisJobSnapshot struct {
	report checksResp
	err    error
	done   bool
}

type analysisManager struct {
	mu        sync.Mutex
	jobs      map[string]*analysisJob
	slots     chan struct{}
	active    int
	maxActive int
}

func newAnalysisManager(concurrency, maxActive int) *analysisManager {
	if concurrency < 1 {
		concurrency = 1
	}
	if maxActive < 1 {
		maxActive = 1
	}
	return &analysisManager{
		jobs:      make(map[string]*analysisJob),
		slots:     make(chan struct{}, concurrency),
		maxActive: maxActive,
	}
}

func (m *analysisManager) start(repo string, restartCompleted bool, run func() (checksResp, error)) error {
	m.mu.Lock()
	m.pruneLocked(time.Now())
	if existing := m.jobs[repo]; existing != nil && (!existing.done || !restartCompleted) {
		m.mu.Unlock()
		return nil
	}
	if m.active >= m.maxActive {
		m.mu.Unlock()
		return errAnalysisQueueFull
	}
	job := &analysisJob{}
	m.jobs[repo] = job
	m.active++
	m.mu.Unlock()

	go m.execute(job, run)
	return nil
}

func (m *analysisManager) execute(job *analysisJob, run func() (checksResp, error)) {
	m.slots <- struct{}{}
	var report checksResp
	var err error
	func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				err = fmt.Errorf("analysis failed unexpectedly: %v", recovered)
			}
		}()
		report, err = run()
	}()
	<-m.slots

	m.mu.Lock()
	job.report = report
	job.err = err
	job.done = true
	job.completedAt = time.Now()
	m.active--
	m.mu.Unlock()
}

func (m *analysisManager) status(repo string) (analysisJobSnapshot, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	job := m.jobs[repo]
	if job == nil {
		return analysisJobSnapshot{}, false
	}
	return analysisJobSnapshot{report: job.report, err: job.err, done: job.done}, true
}

func (m *analysisManager) pruneLocked(now time.Time) {
	for repo, job := range m.jobs {
		if job.done && now.Sub(job.completedAt) > analysisJobTTL {
			delete(m.jobs, repo)
		}
	}
	for len(m.jobs) >= maxAnalysisHistory {
		var oldestRepo string
		var oldest time.Time
		for repo, job := range m.jobs {
			if job.done && (oldest.IsZero() || job.completedAt.Before(oldest)) {
				oldestRepo = repo
				oldest = job.completedAt
			}
		}
		if oldestRepo == "" {
			return
		}
		delete(m.jobs, oldestRepo)
	}
}

var repositoryAnalysisJobs = newAnalysisManager(analysisConcurrency, maxActiveAnalyses)

type checkJobResponse struct {
	Pending      bool        `json:"pending,omitempty"`
	RetryAfterMS int         `json:"retry_after_ms,omitempty"`
	Redirect     string      `json:"redirect,omitempty"`
	Report       *checksResp `json:"report,omitempty"`
}

func runRepositoryAnalysis(db *badger.DB, repo string, forceRefresh bool) (checksResp, error) {
	client := download.NewProxyClient("https://proxy.golang.org")
	moduleName, err := client.ModuleName(repo)
	if err != nil {
		log.Println("ERROR: could not get module name:", err)
	}
	if moduleName != "" {
		repo = moduleName
	}

	log.Printf("Checking repo %q...", repo)
	resp, err := newChecksResp(db, repo, forceRefresh)
	if err != nil {
		return checksResp{}, fmt.Errorf("could not analyze the repository: %w", err)
	}
	return resp, nil
}

func handleChecks(w http.ResponseWriter, r *http.Request, db *badger.DB, repo string) {
	analyze := func(repo string, forceRefresh bool) (checksResp, error) {
		return runRepositoryAnalysis(db, repo, forceRefresh)
	}
	handleChecksWithManager(w, r, db, repo, repositoryAnalysisJobs, analyze)
}

func handleChecksWithManager(w http.ResponseWriter, r *http.Request, db *badger.DB, repo string, manager *analysisManager, analyze func(string, bool) (checksResp, error)) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	forceRefresh := r.Method == http.MethodPost || r.URL.Query().Get("refresh") == "1"

	if !forceRefresh {
		if _, err := getFromCache(db, repo); err == nil {
			writeCheckJobResponse(w, http.StatusOK, checkJobResponse{Redirect: "/report/" + repo})
			return
		} else {
			var missing notFoundError
			if !errors.As(err, &missing) {
				http.Error(w, "Could not read report cache: "+err.Error(), http.StatusInternalServerError)
				return
			}
		}
	}

	restartCompleted := r.Method == http.MethodPost
	run := func() (checksResp, error) {
		return analyze(repo, forceRefresh)
	}
	if err := manager.start(repo, restartCompleted, run); err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}

	if r.Method == http.MethodPost {
		writeCheckJobResponse(w, http.StatusOK, checkJobResponse{Redirect: "/report/" + repo + "?wait=1"})
		return
	}

	job, ok := manager.status(repo)
	if !ok {
		http.Error(w, "analysis job is no longer available; retry the request", http.StatusServiceUnavailable)
		return
	}
	if !job.done {
		writeCheckJobResponse(w, http.StatusAccepted, checkJobResponse{Pending: true, RetryAfterMS: 2000})
		return
	}
	if job.err != nil {
		http.Error(w, job.err.Error(), http.StatusBadRequest)
		return
	}

	redirect := "/report/" + job.report.Repo
	writeCheckJobResponse(w, http.StatusOK, checkJobResponse{Report: &job.report, Redirect: redirect})
}

func writeCheckJobResponse(w http.ResponseWriter, status int, response checkJobResponse) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(response); err != nil {
		log.Printf("ERROR: could not write checks response: %v", err)
	}
}

func validateRepositoryPath(repo string) error {
	if repo == "" {
		return fmt.Errorf("please enter a repository")
	}
	if _, err := module.EscapePath(repo); err != nil {
		return fmt.Errorf("invalid module path: %w", err)
	}
	return nil
}

func CheckHandler(w http.ResponseWriter, r *http.Request, db *badger.DB) {
	repo := download.Clean(r.FormValue("repo"))
	if err := validateRepositoryPath(repo); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	log.Printf("Queueing analysis for repo %q...", repo)
	handleChecks(w, r, db, repo)
}
