package handlers

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/dustin/go-humanize"
	"github.com/lbenicio/goreportcard/check"
	"github.com/lbenicio/goreportcard/download"
	"github.com/lbenicio/goreportcard/storage"
)

type notFoundError struct {
	repo string
}

func (n notFoundError) Error() string {
	return fmt.Sprintf("%q not found in cache", n.repo)
}

func (notFoundError) Unwrap() error {
	return storage.ErrReportNotFound
}

func getFromCache(ctx context.Context, db storage.Store, repo string) (checksResp, error) {
	resp, err := db.GetReport(ctx, repo)
	if errors.Is(err, storage.ErrReportNotFound) {
		log.Printf("Repo %q not found in database cache", repo)
		return checksResp{}, notFoundError{repo: repo}
	}
	if err != nil {
		return checksResp{}, fmt.Errorf("could not read cached report %q: %w", repo, err)
	}

	resp.LastRefresh = resp.LastRefresh.UTC()
	resp.LastRefreshFormatted = resp.LastRefresh.Format(time.UnixDate)
	resp.LastRefreshHumanized = humanize.Time(resp.LastRefresh)
	if resp.Grade == "" {
		resp.Grade = check.GradeFromPercentage(resp.Average * 100)
	}
	return resp, nil
}

func newChecksResp(ctx context.Context, db storage.Store, repo string, forceRefresh bool) (checksResp, error) {
	if err := validateRepositoryPath(repo); err != nil {
		return checksResp{}, err
	}

	if !forceRefresh {
		resp, err := getFromCache(ctx, db, repo)
		if err == nil {
			return resp, nil
		}
		var missing notFoundError
		if !errors.As(err, &missing) {
			return checksResp{}, err
		}
	}

	client := download.NewProxyClient("https://proxy.golang.org")
	downloaded, err := client.Download(repo)
	if err != nil {
		log.Println("ERROR:", err)
		return checksResp{}, fmt.Errorf("could not download repo: %v", err)
	}
	defer func() {
		if err := downloaded.Cleanup(); err != nil {
			log.Println("ERROR: could not remove downloaded module:", err)
		}
	}()

	checkResult, err := check.Run(downloaded.Dir, false)
	if err != nil {
		return checksResp{}, err
	}

	now := time.Now().UTC()
	resp := checksResp{
		Checks:               checkResult.Checks,
		Average:              checkResult.Average,
		Grade:                checkResult.Grade,
		Files:                checkResult.Files,
		Issues:               checkResult.Issues,
		Repo:                 repo,
		Version:              downloaded.Version,
		ResolvedRepo:         repo,
		LastRefresh:          now,
		LastRefreshFormatted: now.Format(time.UnixDate),
		LastRefreshHumanized: humanize.Time(now),
		DidError:             checkResult.DidError,
	}

	if err := db.SaveReport(ctx, resp); err != nil {
		return checksResp{}, fmt.Errorf("could not save report %q: %w", repo, err)
	}
	if err := db.AddRecent(ctx, repo); err != nil {
		log.Printf("ERROR: could not update recently viewed repository: %v", err)
	}

	return resp, nil
}
