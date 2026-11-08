package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/dgraph-io/badger/v2"
	"github.com/lbenicio/goreportcard/storage"
)

const legacyRepoPrefix = "repos-"

type legacyRecentItem struct {
	Repo string
}

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	badgerPath := flag.String("badger-path", "", "path to the existing Badger database")
	flag.Parse()
	if *badgerPath == "" {
		return errors.New("-badger-path is required")
	}
	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		return errors.New("DATABASE_URL is required")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	postgres, err := storage.OpenPostgres(ctx, databaseURL)
	if err != nil {
		return err
	}
	defer postgres.Close()

	legacy, err := badger.Open(badger.DefaultOptions(*badgerPath).WithReadOnly(true).WithLogger(nil))
	if err != nil {
		return fmt.Errorf("open legacy Badger database: %w", err)
	}
	defer legacy.Close()

	imported, err := importReports(ctx, legacy, postgres)
	if err != nil {
		return err
	}

	recent, err := readLegacyRecent(legacy)
	if err != nil {
		return err
	}
	recentImported := 0
	for _, item := range recent {
		if err := postgres.AddRecent(ctx, item.Repo); err != nil {
			if errors.Is(err, storage.ErrRepositoryNotFound) {
				log.Printf("Skipping recent repository %q because it has no imported report", item.Repo)
				continue
			}
			return fmt.Errorf("import recent repository %q: %w", item.Repo, err)
		}
		recentImported++
	}

	log.Printf("Imported %d reports and %d recent repository entries", imported, recentImported)
	return nil
}

func importReports(ctx context.Context, legacy *badger.DB, postgres storage.Store) (int, error) {
	txn := legacy.NewTransaction(false)
	defer txn.Discard()

	options := badger.DefaultIteratorOptions
	options.Prefix = []byte(legacyRepoPrefix)
	iterator := txn.NewIterator(options)
	defer iterator.Close()

	count := 0
	for iterator.Rewind(); iterator.Valid(); iterator.Next() {
		item := iterator.Item()
		key := string(item.KeyCopy(nil))
		repo := strings.TrimPrefix(key, legacyRepoPrefix)
		if repo == "" || repo == key {
			return count, fmt.Errorf("unexpected Badger report key %q", key)
		}

		var report storage.Report
		if err := item.Value(func(value []byte) error {
			return json.Unmarshal(value, &report)
		}); err != nil {
			return count, fmt.Errorf("decode Badger report %q: %w", repo, err)
		}
		report.Repo = repo
		if err := postgres.SaveReport(ctx, report); err != nil {
			return count, fmt.Errorf("import report %q: %w", repo, err)
		}
		count++
	}

	return count, nil
}

func readLegacyRecent(legacy *badger.DB) ([]legacyRecentItem, error) {
	var recent []legacyRecentItem
	err := legacy.View(func(txn *badger.Txn) error {
		item, err := txn.Get([]byte("recent"))
		if errors.Is(err, badger.ErrKeyNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		return item.Value(func(value []byte) error {
			return json.Unmarshal(value, &recent)
		})
	})
	if err != nil {
		return nil, fmt.Errorf("read legacy recent repositories: %w", err)
	}
	return recent, nil
}
