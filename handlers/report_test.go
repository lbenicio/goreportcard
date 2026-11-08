package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/lbenicio/goreportcard/storage"
)

type testStore struct {
	reports map[string]storage.Report
	recent  []string
	count   int64
	scores  []storage.HighScore
}

func (s *testStore) GetReport(_ context.Context, repo string) (storage.Report, error) {
	report, ok := s.reports[repo]
	if !ok {
		return storage.Report{}, storage.ErrReportNotFound
	}
	return report, nil
}

func (s *testStore) SaveReport(_ context.Context, report storage.Report) error {
	if s.reports == nil {
		s.reports = make(map[string]storage.Report)
	}
	s.reports[report.Repo] = report
	return nil
}

func (s *testStore) AddRecent(_ context.Context, repo string) error {
	for _, existing := range s.recent {
		if existing == repo {
			return nil
		}
	}
	s.recent = append(s.recent, repo)
	if len(s.recent) > 5 {
		s.recent = s.recent[len(s.recent)-5:]
	}
	return nil
}

func (s *testStore) RecentRepos(context.Context) ([]string, error) {
	repos := make([]string, len(s.recent))
	for i := range s.recent {
		repos[len(repos)-1-i] = s.recent[i]
	}
	return repos, nil
}

func (s *testStore) HighScores(context.Context) (int64, []storage.HighScore, error) {
	return s.count, s.scores, nil
}

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
	for _, removed := range []string{"Supporters", "Patreon", "Sponsored", "shawnps", "ironzeb", "Production Go"} {
		if strings.Contains(body, removed) {
			t.Errorf("page still contains removed content %q", removed)
		}
	}
}

func TestHomePageUsesMaintainerUI(t *testing.T) {
	cache.mux.Lock()
	cache.items = nil
	cache.count = 0
	cache.mux.Unlock()

	handler := GRCHandler{AssetsFS: http.Dir("../assets")}
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	response := httptest.NewRecorder()
	handler.HomeHandler(response, request, &testStore{})

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
	handler := GRCHandler{AssetsFS: http.Dir("../assets")}
	request := httptest.NewRequest(http.MethodGet, "/report/github.com/example/repo?wait=1", nil)
	response := httptest.NewRecorder()
	handler.ReportHandler(response, request, &testStore{}, "github.com/example/repo")

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", response.Code, http.StatusOK, response.Body.String())
	}
	body := response.Body.String()
	assertMaintainerUI(t, body)
	for _, want := range []string{
		`image_url: "https://goreportcard.lbenicio.dev/badge/"`,
		`url: "https://goreportcard.lbenicio.dev/report/"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("report HTML does not contain generated link %q", want)
		}
	}
	if strings.Contains(body, `https://goreportcard.com/badge/`) {
		t.Error("generated badge URL still points at the retired goreportcard.com host")
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
