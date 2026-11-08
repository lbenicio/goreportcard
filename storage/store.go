package storage

import (
	"context"
	"errors"
	"time"

	"github.com/gojp/goreportcard/check"
)

var (
	ErrReportNotFound     = errors.New("report not found")
	ErrRepositoryNotFound = errors.New("repository not found")
)

// Report is the persisted, user-facing result for a repository.
type Report struct {
	Checks               []check.Score `json:"checks"`
	Average              float64       `json:"average"`
	Grade                check.Grade   `json:"grade"`
	Files                int           `json:"files"`
	Issues               int           `json:"issues"`
	Repo                 string        `json:"repo"`
	Version              string        `json:"version"`
	ResolvedRepo         string        `json:"resolvedRepo"`
	LastRefresh          time.Time     `json:"last_refresh"`
	LastRefreshFormatted string        `json:"formatted_last_refresh"`
	LastRefreshHumanized string        `json:"humanized_last_refresh"`
	DidError             bool          `json:"did_error"`
}

// HighScore is a repository's current score in the leaderboard.
type HighScore struct {
	Repo  string
	Score float64
	Files int
}

// Store defines the relational persistence operations used by the web handlers.
type Store interface {
	GetReport(context.Context, string) (Report, error)
	SaveReport(context.Context, Report) error
	AddRecent(context.Context, string) error
	RecentRepos(context.Context) ([]string, error)
	HighScores(context.Context) (int64, []HighScore, error)
}
