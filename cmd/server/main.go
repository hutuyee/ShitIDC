package main

import (
	"context"
	"log"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/hutuyee/ShitIDC/internal/api"
	"github.com/hutuyee/ShitIDC/internal/cache"
	"github.com/hutuyee/ShitIDC/internal/config"
	"github.com/hutuyee/ShitIDC/internal/database"
	"github.com/hutuyee/ShitIDC/internal/events"
	"github.com/hutuyee/ShitIDC/internal/extension"
	"github.com/hutuyee/ShitIDC/internal/notify"
	"github.com/hutuyee/ShitIDC/internal/queue"
	"github.com/hutuyee/ShitIDC/internal/security"
	"github.com/hutuyee/ShitIDC/internal/store"
	"github.com/hutuyee/ShitIDC/internal/webhook"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatal(err)
	}
	setupLogging(cfg.AppEnv)
	ctx := context.Background()
	db, err := database.Open(ctx, cfg.PostgresDSN)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()
	rdb, err := cache.Open(ctx, cfg.RedisAddr, cfg.RedisPassword, cfg.RedisDB)
	if err != nil {
		slog.Warn("redis unavailable, rate limiting/queue degraded", "error", err)
		rdb = nil
	} else {
		defer rdb.Close()
	}
	st := store.New(db).WithMasterKey(cfg.MasterKey)
	if cfg.BootstrapEmail != "" && cfg.BootstrapPassword != "" {
		hash, e := security.HashPassword(cfg.BootstrapPassword)
		if e != nil {
			log.Fatal(e)
		}
		if e = st.EnsureAdmin(ctx, cfg.BootstrapEmail, hash); e != nil {
			slog.Error("bootstrap admin failed", "error", e)
			os.Exit(1)
		}
	}
	q := queue.New(cfg.RedisAddr, cfg.RedisPassword, cfg.RedisDB)
	defer q.Close()
	// Core hook bus: webhooks, the notification center and WASM extensions
	// all subscribe here (第十一阶段 Hook 系统).
	bus := events.New()
	bus.Subscribe(webhook.Fanout(st, q, func(s string) (string, error) { return security.Decrypt(cfg.MasterKey, s) }))
	extHost := extension.NewHost(st)
	defer extHost.Close()
	// Load active extension packages (upload + enable from the admin console).
	if actives, err := st.ActiveExtensions(ctx); err != nil {
		slog.Warn("extension load skipped", "error", err)
	} else {
		for _, ext := range actives {
			wasm, err := os.ReadFile(ext.SourcePath)
			if err != nil {
				slog.Warn("extension module missing", "name", ext.Name, "error", err)
				continue
			}
			if _, err := extHost.Load(ctx, extension.Manifest{Name: ext.Name, Version: ext.Version, Description: ext.Description, Entry: filepath.Base(ext.SourcePath), Permissions: ext.Permissions, Events: nil}, wasm); err != nil {
				slog.Warn("extension load failed", "name", ext.Name, "error", err)
				continue
			}
			slog.Info("extension loaded", "name", ext.Name, "version", ext.Version)
		}
	}
	notify.Install(bus, st, extHost)
	app := &api.App{Store: st, Redis: rdb, Queue: q, Cfg: cfg, Bus: bus, ExtHost: extHost}
	notify.InstallAdminMail(bus, st, app.DeliverMailVia)
	router := api.NewRouter(app)
	srv := &http.Server{Addr: cfg.HTTPAddr, Handler: router, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 20 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 90 * time.Second, MaxHeaderBytes: 1 << 20}
	go func() {
		slog.Info("ShitIDC API listening", "addr", cfg.HTTPAddr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("listen failed", "error", err)
			os.Exit(1)
		}
	}()
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop
	ctxShutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = srv.Shutdown(ctxShutdown)
}

// setupLogging switches to structured JSON logs in production (第十七阶段).
func setupLogging(env string) {
	if env == "production" {
		slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})))
	} else {
		slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelDebug})))
	}
}
