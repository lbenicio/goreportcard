package handlers

import (
	"log"
	"net/http"

	"github.com/dustin/go-humanize"
	"github.com/gojp/goreportcard/storage"
)

// HighScoresHandler handles the stats page
func (gh *GRCHandler) HighScoresHandler(w http.ResponseWriter, r *http.Request, db storage.Store) {
	count, scores, err := db.HighScores(r.Context())
	if err != nil {
		log.Println("ERROR: Failed to load high scores from PostgreSQL: ", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	t, err := gh.loadTemplate("/templates/high_scores.html")
	if err != nil {
		log.Println("ERROR: could not get high scores template: ", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	sortedScores := make([]scoreItem, len(scores))
	for i, score := range scores {
		sortedScores[i] = scoreItem{Repo: score.Repo, Score: score.Score, Files: score.Files}
	}

	if err := t.ExecuteTemplate(w, "base", map[string]interface{}{
		"HighScores":           sortedScores,
		"Count":                humanize.Comma(count),
		"google_analytics_key": googleAnalyticsKey,
	}); err != nil {
		log.Println("ERROR:", err)
	}
}
