package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/lbenicio/goreportcard/check"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

func TestReportJSONKeepsLegacyGradeField(t *testing.T) {
	data, err := json.Marshal(Report{Grade: check.GradeA})
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		t.Fatal(err)
	}
	if _, ok := fields["grade"]; !ok {
		t.Fatalf("JSON report does not contain legacy grade field: %s", data)
	}
	if _, ok := fields["GradeFromPercentage"]; ok {
		t.Fatalf("JSON report contains unexpected grade field: %s", data)
	}
}

func TestPostgresStoreRelationalRoundTrip(t *testing.T) {
	store := newTestPostgresStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	firstRefresh := time.Date(2026, time.January, 2, 3, 4, 5, 0, time.FixedZone("test", 2*60*60))
	first := Report{
		Checks: []check.Score{
			{
				Name:        "gofmt",
				Description: "formatting check",
				Weight:      0.25,
				Percentage:  0.95,
				FileSummaries: []check.FileSummary{
					{
						Filename: "main.go",
						FileURL:  "https://example.test/main.go",
						Errors: []check.Error{
							{LineNumber: 12, ErrorString: "formatting issue"},
							{LineNumber: 18, ErrorString: "another issue"},
						},
					},
					{Filename: "clean.go", FileURL: "https://example.test/clean.go", Errors: []check.Error{}},
				},
			},
			{Name: "license", Description: "license check", Weight: 0.1, Percentage: 1, FileSummaries: []check.FileSummary{}},
		},
		Average:      0.95,
		Grade:        check.GradeAPlus,
		Files:        100,
		Issues:       1,
		Repo:         "github.com/example/first",
		Version:      "v1.2.3",
		ResolvedRepo: "github.com/example/first",
		LastRefresh:  firstRefresh,
		DidError:     true,
	}
	if err := store.SaveReport(ctx, first); err != nil {
		t.Fatal(err)
	}

	got, err := store.GetReport(ctx, first.Repo)
	if err != nil {
		t.Fatal(err)
	}
	first.LastRefresh = firstRefresh.UTC()
	if !reflect.DeepEqual(got, first) {
		t.Fatalf("report round trip mismatch:\n got: %#v\nwant: %#v", got, first)
	}

	for i := 2; i <= 6; i++ {
		repo := fmt.Sprintf("github.com/example/repo-%d", i)
		report := Report{
			Repo:         repo,
			ResolvedRepo: repo,
			Average:      float64(100-i) / 100,
			Grade:        check.GradeFromPercentage(float64(100 - i)),
			Files:        100,
			LastRefresh:  time.Now().UTC(),
		}
		if err := store.SaveReport(ctx, report); err != nil {
			t.Fatal(err)
		}
		if err := store.AddRecent(ctx, repo); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.AddRecent(ctx, "github.com/example/repo-2"); err != nil {
		t.Fatal(err)
	}
	if err := store.AddRecent(ctx, first.Repo); err != nil {
		t.Fatal(err)
	}

	recent, err := store.RecentRepos(ctx)
	if err != nil {
		t.Fatal(err)
	}
	wantRecent := []string{
		"github.com/example/first",
		"github.com/example/repo-6",
		"github.com/example/repo-5",
		"github.com/example/repo-4",
		"github.com/example/repo-3",
	}
	if !reflect.DeepEqual(recent, wantRecent) {
		t.Fatalf("recent repositories = %v, want %v", recent, wantRecent)
	}

	count, scores, err := store.HighScores(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if count != 6 {
		t.Errorf("repository count = %d, want 6", count)
	}
	if len(scores) != 5 || scores[0].Repo != "github.com/example/repo-2" || scores[0].Score != 98 {
		t.Errorf("leaderboard = %+v, want repo-2 at the top and 5 entries", scores)
	}

	first.Checks = []check.Score{{Name: "replacement", Percentage: 0.8}}
	first.Average = 0.8
	first.Grade = check.GradeFromPercentage(80)
	first.Files = 50
	first.LastRefresh = firstRefresh.Add(time.Hour)
	first.DidError = false
	if err := store.SaveReport(ctx, first); err != nil {
		t.Fatal(err)
	}
	got, err = store.GetReport(ctx, first.Repo)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Checks) != 1 || got.Checks[0].Name != "replacement" || len(got.Checks[0].FileSummaries) != 0 {
		t.Errorf("replaced report checks = %+v, want only replacement check", got.Checks)
	}
	count, scores, err = store.HighScores(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if count != 6 {
		t.Errorf("repository count after refresh = %d, want 6", count)
	}
	for _, score := range scores {
		if score.Repo == first.Repo {
			t.Errorf("report below the file threshold remains in leaderboard: %+v", score)
		}
	}
}

func TestPostgresStoreDeletesRepositoryAndFindsCaseDuplicates(t *testing.T) {
	store := newTestPostgresStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	canonical := Report{Repo: "github.com/example/repo", ResolvedRepo: "github.com/example/repo"}
	duplicate := Report{Repo: "github.com/Example/repo", ResolvedRepo: "github.com/Example/repo"}
	if err := store.SaveReport(ctx, canonical); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveReport(ctx, duplicate); err != nil {
		t.Fatal(err)
	}

	duplicates, err := store.DuplicateRepositoryPaths(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(duplicates, []string{duplicate.Repo}) {
		t.Fatalf("case duplicates = %v, want [%q]", duplicates, duplicate.Repo)
	}

	deleted, err := store.DeleteRepository(ctx, duplicate.Repo)
	if err != nil {
		t.Fatal(err)
	}
	if !deleted {
		t.Fatal("DeleteRepository returned false for an existing repository")
	}
	if _, err := store.GetReport(ctx, duplicate.Repo); !errors.Is(err, ErrReportNotFound) {
		t.Fatalf("GetReport after deletion error = %v, want ErrReportNotFound", err)
	}
	count, _, err := store.HighScores(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Errorf("repository count after deletion = %d, want 1", count)
	}
}

func newTestPostgresStore(t *testing.T) *PostgresStore {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set TEST_DATABASE_URL to run PostgreSQL integration tests")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	adminDB, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	if err := adminDB.PingContext(ctx); err != nil {
		adminDB.Close()
		t.Fatalf("connect to TEST_DATABASE_URL: %v", err)
	}

	schema := fmt.Sprintf("goreportcard_test_%d_%d", os.Getpid(), time.Now().UnixNano())
	if _, err := adminDB.ExecContext(ctx, "CREATE SCHEMA \""+schema+"\""); err != nil {
		adminDB.Close()
		t.Fatalf("create temporary test schema: %v", err)
	}

	config, err := pgx.ParseConfig(dsn)
	if err != nil {
		adminDB.ExecContext(ctx, "DROP SCHEMA \""+schema+"\" CASCADE")
		adminDB.Close()
		t.Fatalf("parse TEST_DATABASE_URL: %v", err)
	}
	config.RuntimeParams["search_path"] = schema
	db := stdlib.OpenDB(*config)
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	store := &PostgresStore{db: db}
	if err := store.Migrate(ctx); err != nil {
		db.Close()
		adminDB.ExecContext(ctx, "DROP SCHEMA \""+schema+"\" CASCADE")
		adminDB.Close()
		t.Fatalf("migrate temporary test schema: %v", err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("close test database: %v", err)
		}
		if _, err := adminDB.ExecContext(context.Background(), "DROP SCHEMA \""+schema+"\" CASCADE"); err != nil {
			t.Errorf("drop temporary test schema: %v", err)
		}
		if err := adminDB.Close(); err != nil {
			t.Errorf("close administrative database connection: %v", err)
		}
	})
	return store
}
