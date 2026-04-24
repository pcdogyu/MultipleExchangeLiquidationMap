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
	if debug {
		log.Printf("debug enabled: db_path=%s addr=%s symbol=%s", dbPath, liqmap.DefaultServerAddr, liqmap.DefaultSymbol)
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

	mux := app.NewRouter(core, debug)
	srv := &http.Server{Addr: liqmap.DefaultServerAddr, Handler: mux}
	go func() {
		<-rootCtx.Done()
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutCtx)
	}()

	log.Printf("dashboard listening on http://127.0.0.1%s", liqmap.DefaultServerAddr)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}
