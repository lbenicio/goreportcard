package handlers

import (
	"context"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/lbenicio/goreportcard/src/storage"
)

var cache struct {
	items       []string
	mux         sync.Mutex
	count       int
	refreshing  bool
	lastAttempt time.Time
	lastUpdated time.Time
}

const (
	recentReposRefreshInterval = 30 * time.Second
	recentReposRefreshTimeout  = 10 * time.Second
)

// recentRepos serves the last snapshot immediately and refreshes it out of band.
// The database read must not inherit a homepage request's short-lived context.
func recentRepos(db storage.Store) []string {
	cache.mux.Lock()
	cache.count++

	shouldRefresh := len(cache.items) != 5 || cache.count >= 100 || time.Since(cache.lastUpdated) >= recentReposRefreshInterval
	if shouldRefresh && !cache.refreshing && time.Since(cache.lastAttempt) >= recentReposRefreshInterval {
		cache.refreshing = true
		cache.lastAttempt = time.Now()
		cache.count = 0
		go refreshRecentRepos(db)
	}
	items := append([]string(nil), cache.items...)
	cache.mux.Unlock()

	return items
}

func refreshRecentRepos(db storage.Store) {
	log.Println("Updating recent repos cache...")
	ctx, cancel := context.WithTimeout(context.Background(), recentReposRefreshTimeout)
	defer cancel()

	items, err := db.RecentRepos(ctx)
	cache.mux.Lock()
	cache.refreshing = false
	if err == nil {
		cache.items = items
		cache.lastUpdated = time.Now()
	}
	cache.mux.Unlock()

	if err != nil {
		log.Printf("ERROR: updating recent repos cache: %v", err)
	}
}

// HomeHandler handles the homepage
func (gh *GRCHandler) HomeHandler(w http.ResponseWriter, r *http.Request, db storage.Store) {
	if r.URL.Path[1:] == "" {
		recent := recentRepos(db)

		t, err := gh.loadTemplate("templates/home.html")
		if err != nil {
			log.Println("ERROR: could not get home template: ", err)
			http.Error(w, err.Error(), 500)
			return
		}

		if err := t.ExecuteTemplate(w, "base", map[string]interface{}{
			"Recent":               recent,
			"google_analytics_key": googleAnalyticsKey,
		}); err != nil {
			log.Println(err)
		}

		return
	}

	gh.errorHandler(w, r, http.StatusNotFound)
}
