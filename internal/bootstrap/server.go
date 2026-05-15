package bootstrap

import (
	"context"
	"database/sql"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"multipleexchangeliquidationmap/internal/app"
	liqmap "multipleexchangeliquidationmap/internal/core"
	dbplatform "multipleexchangeliquidationmap/internal/platform/db"

	_ "modernc.org/sqlite"
)

func Run() {
	debug := liqmap.Getenv("DEBUG", "") != ""
	cleanupLogging, err := liqmap.SetupLogging(debug)
	if err != nil {
		log.Fatal(err)
	}
	defer cleanupLogging()

	dbPath := liqmap.Getenv("DB_PATH", liqmap.DefaultDBPath)
	addr := serverAddrFromEnv()
	log.Printf("version info: branch=%s commit=%s commit_time=%s", versionEnv("VERSION_BRANCH"), versionEnv("VERSION_COMMIT"), versionEnv("VERSION_COMMIT_TIME"))
	if debug {
		log.Printf("debug enabled: db_path=%s addr=%s symbol=%s", dbPath, addr, liqmap.DefaultSymbol)
	}
	if dir := filepath.Dir(dbPath); dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			log.Fatal(err)
		}
	}

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	if err := dbplatform.Configure(db); err != nil {
		log.Fatal(err)
	}
	if err := dbplatform.Init(db); err != nil {
		log.Fatal(err)
	}

	core := liqmap.NewApp(db, debug)

	rootCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	core.StartBackgroundJobs(rootCtx)
	go func() {
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-rootCtx.Done():
				return
			case <-ticker.C:
				log.Printf("heartbeat: server alive addr=%s", addr)
			}
		}
	}()

	mux := app.NewRouter(core, debug)
	srv := &http.Server{Addr: addr, Handler: mux}
	go func() {
		<-rootCtx.Done()
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutCtx)
	}()

	log.Printf("dashboard listening on http://127.0.0.1%s", addr)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}

func serverAddrFromEnv() string {
	if raw := strings.TrimSpace(os.Getenv("APP_ADDR")); raw != "" {
		if strings.Contains(raw, ":") {
			return raw
		}
		return ":" + raw
	}
	if raw := strings.TrimSpace(os.Getenv("APP_PORT")); raw != "" {
		raw = strings.TrimPrefix(raw, ":")
		if raw != "" {
			return ":" + raw
		}
	}
	return liqmap.DefaultServerAddr
}

func versionEnv(key string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return "-"
}
