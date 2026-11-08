package handlers

import (
	"log"
	"net/http"
	"sync"

	"github.com/gojp/goreportcard/storage"
)

var cache struct {
	items []string
	mux   sync.Mutex
	count int
}

// HomeHandler handles the homepage
func (gh *GRCHandler) HomeHandler(w http.ResponseWriter, r *http.Request, db storage.Store) {
	if r.URL.Path[1:] == "" {
		var recentRepos []string

		cache.mux.Lock()
		cache.count++
		defer cache.mux.Unlock()

		if cache.count < 100 && len(cache.items) == 5 {
			recentRepos = cache.items
			log.Println("Fetching recent repos from cache...")
		} else {
			log.Println("Updating recent repos cache...")
			var err error
			recentRepos, err = db.RecentRepos(r.Context())
			if err != nil {
				log.Println("ERROR: ", err)
				recentRepos = nil
			}
			cache.items = recentRepos
			cache.count = 0
		}

		t, err := gh.loadTemplate("templates/home.html")
		if err != nil {
			log.Println("ERROR: could not get home template: ", err)
			http.Error(w, err.Error(), 500)
			return
		}

		if err := t.ExecuteTemplate(w, "base", map[string]interface{}{
			"Recent":               recentRepos,
			"google_analytics_key": googleAnalyticsKey,
		}); err != nil {
			log.Println(err)
		}

		return
	}

	gh.errorHandler(w, r, http.StatusNotFound)
}
