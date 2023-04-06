package main

import (
	"github.com/asaskevich/EventBus"
	"github.com/kaiserbh/tachiyomi-sync-server/internal/api"
	"github.com/kaiserbh/tachiyomi-sync-server/internal/auth"
	"github.com/kaiserbh/tachiyomi-sync-server/internal/config"
	"github.com/kaiserbh/tachiyomi-sync-server/internal/database"
	"github.com/kaiserbh/tachiyomi-sync-server/internal/events"
	"github.com/kaiserbh/tachiyomi-sync-server/internal/http"
	"github.com/kaiserbh/tachiyomi-sync-server/internal/logger"
	"github.com/kaiserbh/tachiyomi-sync-server/internal/notification"
	"github.com/kaiserbh/tachiyomi-sync-server/internal/scheduler"
	"github.com/kaiserbh/tachiyomi-sync-server/internal/server"
	"github.com/kaiserbh/tachiyomi-sync-server/internal/update"
	"github.com/kaiserbh/tachiyomi-sync-server/internal/user"
	"github.com/r3labs/sse/v2"
	"github.com/spf13/pflag"
	"os"
	"os/signal"
	"syscall"
)

var (
	version = "dev"
	commit  = ""
	date    = ""
)

func main() {
	var configPath string
	pflag.StringVar(&configPath, "config", "", "path to configuration file")
	pflag.Parse()

	// read config
	cfg := config.New(configPath, version)

	// init new logger
	log := logger.New(cfg.Config)

	// init dynamic config
	cfg.DynamicReload(log)

	// setup server-sent-events
	serverEvents := sse.New()
	serverEvents.AutoReplay = false
	serverEvents.CreateStream("logs")

	// register SSE hook on logger
	log.RegisterSSEHook(serverEvents)

	// setup internal eventbus
	bus := EventBus.New()

	// open database connection
	db, _ := database.NewDB(cfg.Config, log)
	if err := db.Open(); err != nil {
		log.Fatal().Err(err).Msg("could not open db connection")
	}

	log.Info().Msgf("Starting Tachiyomi Sync Server")
	log.Info().Msgf("Version: %s", version)
	log.Info().Msgf("Commit: %s", commit)
	log.Info().Msgf("Build date: %s", date)
	log.Info().Msgf("Log-level: %s", cfg.Config.LogLevel)
	log.Info().Msgf("Using database: %s", db.Driver)

	// setup repos
	var (
		apikeyRepo       = database.NewAPIRepo(log, db)
		notificationRepo = database.NewNotificationRepo(log, db)
		userRepo         = database.NewUserRepo(log, db)
	)

	// setup services
	var (
		apiService          = api.NewService(log, apikeyRepo)
		notificationService = notification.NewService(log, notificationRepo)
		updateService       = update.NewUpdate(log, cfg.Config)
		schedulingService   = scheduler.NewService(log, cfg.Config, notificationService, updateService)
		userService         = user.NewService(userRepo)
		authService         = auth.NewService(log, userService)
	)

	// register event subscribers
	events.NewSubscribers(log, bus, notificationService)

	errorChannel := make(chan error)

	go func() {
		httpServer := http.NewServer(
			log,
			cfg,
			serverEvents,
			db,
			version,
			commit,
			date,
			apiService,
			authService,
			notificationService,
			updateService,
		)
		errorChannel <- httpServer.Open()
	}()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGHUP, syscall.SIGINT, syscall.SIGQUIT, syscall.SIGKILL, syscall.SIGTERM)

	srv := server.NewServer(log, cfg.Config, schedulingService, updateService)
	if err := srv.Start(); err != nil {
		log.Fatal().Stack().Err(err).Msg("could not start server")
		return
	}

	for sig := range sigCh {
		switch sig {
		case syscall.SIGHUP:
			log.Log().Msg("shutting down server sighup")
			srv.Shutdown()
			err := db.Close()
			if err != nil {
				log.Fatal().Stack().Err(err).Msg("could not close db connection")
				return
			}
			os.Exit(1)
		case syscall.SIGINT, syscall.SIGQUIT:
			srv.Shutdown()
			err := db.Close()
			if err != nil {
				log.Fatal().Stack().Err(err).Msg("could not close db connection")
				return
			}
			os.Exit(1)
		case syscall.SIGKILL, syscall.SIGTERM:
			srv.Shutdown()
			err := db.Close()
			if err != nil {
				log.Fatal().Stack().Err(err).Msg("could not close db connection")
				return
			}
			os.Exit(1)
		}
	}
}
