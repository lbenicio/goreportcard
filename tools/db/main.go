package main

import (
	"context"
	"errors"
	"flag"
	"log"
	"os"
	"time"

	"github.com/lbenicio/goreportcard/storage"
)

var (
	deleteRepoName       = flag.String("deleterepo", "", "repository path to delete from the database")
	removeDuplicatesFlag = flag.Bool("removeduplicates", false, "remove non-lowercase duplicates when lowercase paths exist")
	dryRun               = flag.Bool("dryrun", false, "dry run mode")
	openRepositoryStore  = func(ctx context.Context, databaseURL string) (repositoryAdminStore, error) {
		return storage.OpenDatabase(ctx, databaseURL)
	}
	fatalLog = log.Fatal
)

type repositoryAdminStore interface {
	DeleteRepository(context.Context, string) (bool, error)
	DuplicateRepositoryPaths(context.Context) ([]string, error)
	Close() error
}

func main() {
	if err := run(); err != nil {
		fatalLog(err)
	}
}

func run() error {
	flag.Parse()
	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		return errors.New("DATABASE_URL is required")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	store, err := openRepositoryStore(ctx, databaseURL)
	if err != nil {
		return err
	}
	defer store.Close()

	if *deleteRepoName != "" {
		if err := deleteRepo(ctx, store, *deleteRepoName, *dryRun); err != nil {
			return err
		}
	}
	if *removeDuplicatesFlag {
		if err := removeDuplicates(ctx, store, *dryRun); err != nil {
			return err
		}
	}
	return nil
}

func deleteRepo(ctx context.Context, store repositoryAdminStore, repo string, dryRun bool) error {
	if dryRun {
		log.Println("would delete", repo)
		return nil
	}
	deleted, err := store.DeleteRepository(ctx, repo)
	if err != nil {
		return err
	}
	if !deleted {
		log.Println("repository not found:", repo)
	}
	return nil
}

func removeDuplicates(ctx context.Context, store repositoryAdminStore, dryRun bool) error {
	duplicates, err := store.DuplicateRepositoryPaths(ctx)
	if err != nil {
		return err
	}
	for _, repo := range duplicates {
		if dryRun {
			log.Println("would delete", repo)
			continue
		}
		deleted, err := store.DeleteRepository(ctx, repo)
		if err != nil {
			return err
		}
		if deleted {
			log.Println("Deleted", repo)
		}
	}
	return nil
}
