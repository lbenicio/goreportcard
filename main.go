package main

import (
	"context"
	"embed"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/lbenicio/goreportcard/handlers"
	"github.com/lbenicio/goreportcard/storage"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var (
	addr = flag.String("http", ":8000", "HTTP listen address")

	//go:embed assets/*
	embedFS embed.FS
)

type closeableStore interface {
	storage.Store
	Close() error
}

var (
	parseFlags = flag.Parse
	fatal      = log.Fatal

	openDatabaseStore = func(ctx context.Context, dsn string) (closeableStore, error) {
		return storage.OpenDatabase(ctx, dsn)
	}
	openRedisStore = func(ctx context.Context, store storage.Store, redisURL string) (closeableStore, error) {
		return storage.OpenRedisCache(ctx, store, redisURL)
	}
	loadAssets = func() (fs.FS, error) {
		return fs.Sub(embedFS, "assets")
	}
	listenAndServe = http.ListenAndServe
)

func makeHandler(db storage.Store, name string, fn func(http.ResponseWriter, *http.Request, storage.Store, string)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		validPath := regexp.MustCompile(fmt.Sprintf(`^/%s/([a-zA-Z0-9\-_\/\.~]*)$`, name))

		m := validPath.FindStringSubmatch(r.URL.Path)
		if m == nil {
			http.NotFound(w, r)
			return
		}
		if len(m) < 1 || m[1] == "" {
			http.Error(w, "Please enter a repository", http.StatusBadRequest)
			return
		}

		repo := m[1]

		// for backwards-compatibility, we must support URLs formatted as
		//   /report/[org]/[repo]
		// and they will be assumed to be github.com URLs. This is because
		// at first Go Report Card only supported github.com URLs, and
		// took only the org name and repo name as parameters. This is no longer the
		// case, but we do not want external links to break.
		oldFormat := regexp.MustCompile(fmt.Sprintf(`^/%s/([a-zA-Z0-9\-_]+)/([a-zA-Z0-9\-_]+)$`, name))
		m2 := oldFormat.FindStringSubmatch(r.URL.Path)
		if m2 != nil {
			// old format is being used
			repo = "github.com/" + repo
			log.Printf("Assuming intended repo is %q, redirecting", repo)
			http.Redirect(w, r, fmt.Sprintf("/%s/%s", name, repo), http.StatusMovedPermanently)
			return
		}

		fn(w, r, db, repo)
	}
}

func injectStoreHandler(db storage.Store, fn func(http.ResponseWriter, *http.Request, storage.Store)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		fn(w, r, db)
	}
}

// metrics provides functionality for monitoring the application status
type metrics struct {
	responseTimes *prometheus.SummaryVec
}

// setupMetrics creates custom Prometheus metrics for monitoring
// application statistics.
func setupMetrics() *metrics {
	return setupMetricsWithRegisterer(prometheus.DefaultRegisterer)
}

func setupMetricsWithRegisterer(registerer prometheus.Registerer) *metrics {
	m := &metrics{}
	m.responseTimes = prometheus.NewSummaryVec(
		prometheus.SummaryOpts{
			Name: "response_time_ms",
			Help: "Time to serve requests",
		},
		[]string{"endpoint"},
	)

	registerer.MustRegister(m.responseTimes)
	return m
}

// recordDuration records the length of a request from start to now
func (m metrics) recordDuration(start time.Time, name string) {
	elapsed := time.Since(start)
	m.responseTimes.WithLabelValues(name).Observe(float64(elapsed.Milliseconds()))
	log.Printf("Served %s in %s", name, elapsed)
}

// instrument adds metric instrumentation to the handler passed in as argument
func (m metrics) instrument(path string, h http.HandlerFunc) (string, http.HandlerFunc) {
	return path, func(w http.ResponseWriter, r *http.Request) {
		defer m.recordDuration(time.Now(), r.URL.Path)
		h.ServeHTTP(w, r)
	}
}

func main() {
	parseFlags()
	if err := run(); err != nil {
		fatal(err)
	}
}

func run() error {
	if err := os.MkdirAll("_repos/src/github.com", 0755); err != nil && !os.IsExist(err) {
		return fmt.Errorf("ERROR: could not create repos dir: %w", err)
	}

	startupCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	db, err := openDatabaseStore(startupCtx, os.Getenv("DATABASE_URL"))
	if err != nil {
		return fmt.Errorf("ERROR: could not open database: %w", err)
	}
	defer db.Close()

	var appStore storage.Store = db
	if redisURL := strings.TrimSpace(os.Getenv("REDIS_URL")); redisURL != "" {
		redisStore, err := openRedisStore(startupCtx, db, redisURL)
		if err != nil {
			return fmt.Errorf("ERROR: could not configure Redis cache: %w", err)
		}
		defer func() {
			if err := redisStore.Close(); err != nil {
				log.Printf("ERROR: could not close Redis cache: %v", err)
			}
		}()
		appStore = redisStore
	}

	assetsFS, err := loadAssets()
	if err != nil {
		return err
	}

	gh := handlers.GRCHandler{AssetsFS: http.FS(assetsFS)}
	registry := prometheus.NewRegistry()
	m := setupMetricsWithRegisterer(registry)
	mux := http.NewServeMux()

	mux.HandleFunc(m.instrument("/assets/", http.StripPrefix("/assets/", http.FileServer(http.FS(assetsFS))).ServeHTTP))
	mux.HandleFunc(m.instrument("/checks", injectStoreHandler(appStore, handlers.CheckHandler)))
	mux.HandleFunc(m.instrument("/report/", makeHandler(appStore, "report", gh.ReportHandler)))
	mux.HandleFunc(m.instrument("/badge/", makeHandler(appStore, "badge", handlers.BadgeHandler)))
	mux.HandleFunc(m.instrument("/high_scores/", injectStoreHandler(appStore, gh.HighScoresHandler)))
	mux.HandleFunc(m.instrument("/about/", gh.AboutHandler))
	mux.HandleFunc(m.instrument("/sunset/", gh.SunsetHandler))
	mux.HandleFunc(m.instrument("/", injectStoreHandler(appStore, gh.HomeHandler)))

	mux.Handle("/metrics", promhttp.HandlerFor(registry, promhttp.HandlerOpts{}))

	log.Printf("Running on %s ...", *addr)
	return listenAndServe(*addr, mux)
}
