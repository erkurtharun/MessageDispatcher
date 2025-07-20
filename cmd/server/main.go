// Package main starts the server application.
package main

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	_ "MessageDispatcher/docs"

	"MessageDispatcher/internal/app/sender"
	"MessageDispatcher/internal/infrastructure/cache"
	"MessageDispatcher/internal/infrastructure/db"
	"MessageDispatcher/internal/infrastructure/http/handlers"
	"MessageDispatcher/internal/infrastructure/http/middleware"
	"MessageDispatcher/internal/infrastructure/scheduler"
	"MessageDispatcher/internal/pkg/config"
	"MessageDispatcher/internal/pkg/dynamicconfig"
	"MessageDispatcher/internal/pkg/logger"
	"MessageDispatcher/internal/pkg/migration"

	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
	"github.com/swaggo/files"
	ginSwagger "github.com/swaggo/gin-swagger"
	"go.uber.org/zap"
	"golang.org/x/sync/errgroup"

	_ "github.com/lib/pq"
)

const (
	defaultEnv  = "local"
	debugMode   = "debug"
	maxAttempts = 5
)

func main() {
	log, loggerInitErr := logger.Init()
	if loggerInitErr != nil {
		_, _ = fmt.Fprintf(os.Stderr, "Logger init failed: %v\n", loggerInitErr)
		os.Exit(1)
	}
	defer func() {
		if err := log.Sync(); err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "Logger sync failed: %v\n", err)
		}
	}()

	env := strings.ToLower(os.Getenv("APP_ENV"))
	if env == "" {
		env = defaultEnv
	}

	configLoader := config.NewConfigLoader(env, log)
	appConfig, cfgErr := configLoader.Load()
	if cfgErr != nil {
		log.Fatal("Config load failed", zap.Error(cfgErr))
	}

	dynamicCfgManager, _ := dynamicconfig.NewManager(log, appConfig.Remote.EtcdEndpoints)
	dynCfg := dynamicCfgManager.Get()
	log.Info("Dynamic config loaded", zap.Bool("auto_sending", dynCfg.AutoSending), zap.Int("rate_limit", dynCfg.RateLimit))

	if strings.ToLower(os.Getenv("GIN_MODE")) != debugMode {
		gin.SetMode(gin.ReleaseMode)
		log.Info("Gin set to release mode")
	} else {
		log.Info("Gin set to debug mode")
	}

	dbPool, dbErr := mustInitDBPool(log, appConfig)
	if dbErr != nil {
		log.Fatal("DB init failed", zap.Error(dbErr))
	}
	defer func() {
		if err := dbPool.Close(); err != nil {
			log.Warn("DB pool close failed", zap.Error(err))
		}
	}()

	mainContext, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := migration.Migrate(mainContext, dbPool, env, log); err != nil {
		log.Fatal("DB migration failed", zap.Error(err))
	}

	redisClient, redisErr := mustInitRedisClient(log, appConfig)
	if redisErr != nil {
		log.Fatal("Redis init failed", zap.Error(redisErr))
	}
	defer func() {
		if err := redisClient.Close(); err != nil {
			log.Warn("Redis client close failed", zap.Error(err))
		}
	}()

	repo := db.NewPostgresRepo(dbPool)
	redisCache := cache.NewRedisCache(redisClient)
	service := sender.NewService(sender.ServiceDeps{
		Repo:          repo,
		Cache:         redisCache,
		WebhookURL:    dynCfg.WebhookURL,
		ConfigManager: dynamicCfgManager,
		Logger:        log,
	})

	schedulerConfig := scheduler.Config{
		Interval:      time.Duration(dynCfg.SchedulerInterval) * time.Second,
		QueueCapacity: dynCfg.SchedulerQueueCapacity,
	}
	schedulerInstance := scheduler.NewScheduler(
		service,
		schedulerConfig,
		log,
		redisCache,
	)
	schedulerInstance.Start(mainContext, false)
	log.Info("Scheduler started")

	router := gin.Default()
	router.Use(middleware.LoggerMiddleware())

	router.GET("/health", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "healthy"})
	})
	router.POST("/start", handlers.StartHandler(schedulerInstance))
	router.POST("/stop", handlers.StopHandler(schedulerInstance))
	router.GET("/sent", handlers.GetSentHandler(service))
	router.GET("/swagger/*any", ginSwagger.WrapHandler(swaggerFiles.Handler))

	server := &http.Server{
		Addr:         appConfig.Port,
		Handler:      router,
		ReadTimeout:  time.Duration(appConfig.Server.ReadTimeout) * time.Second,
		WriteTimeout: time.Duration(appConfig.Server.WriteTimeout) * time.Second,
		IdleTimeout:  time.Duration(appConfig.Server.IdleTimeout) * time.Second,
	}

	g, gCtx := errgroup.WithContext(context.Background())
	g.Go(func() error {
		log.Info("Starting server", zap.String("addr", server.Addr))
		if os.Getenv("USE_HTTPS") == "true" {
			return server.ListenAndServeTLS(os.Getenv("TLS_CERT"), os.Getenv("TLS_KEY"))
		}
		return server.ListenAndServe()
	})

	g.Go(func() error {
		sig := <-shutdownSignalChan()
		log.Info("Shutdown signal received", zap.String("signal", sig.String()))
		cancel()
		schedulerInstance.Stop()
		shutdownCtx, shutdownCancel := context.WithTimeout(gCtx, 15*time.Second)
		defer shutdownCancel()
		return server.Shutdown(shutdownCtx)
	})

	if err := g.Wait(); err != nil {
		log.Error("Shutdown error", zap.Error(err))
		os.Exit(1)
	}
	log.Info("Shutdown gracefully")
}

func shutdownSignalChan() chan os.Signal {
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	return sigChan
}

func mustInitDBPool(log *zap.Logger, cfg *config.StaticConfig) (*sql.DB, error) {
	var dbPool *sql.DB
	var err error
	backoff := 1 * time.Second
	loggedDSN := maskDSN(cfg.DB.URL)

	log.Debug("DB config",
		zap.String("dsn", loggedDSN),
		zap.Int("max_open_connections", cfg.DB.MaxOpenConnections),
		zap.Int("max_idle_connections", cfg.DB.MaxIdleConnections),
		zap.Int("conn_max_lifetime_sec", cfg.DB.ConnMaxLifetime),
	)

	for attempt := 1; attempt <= maxAttempts; attempt++ {
		log.Info("Attempting to open DB connection", zap.String("dsn", loggedDSN), zap.Int("attempt", attempt))
		dbPool, err = sql.Open("postgres", cfg.DB.URL)
		if err != nil {
			log.Error("DB open failed", zap.Error(err), zap.Int("attempt", attempt))
			time.Sleep(backoff)
			backoff *= 2
			continue
		}
		dbPool.SetMaxOpenConns(cfg.DB.MaxOpenConnections)
		dbPool.SetMaxIdleConns(cfg.DB.MaxIdleConnections)
		dbPool.SetConnMaxLifetime(time.Duration(cfg.DB.ConnMaxLifetime) * time.Second)
		log.Debug("DB connection opened, testing ping...", zap.Int("attempt", attempt))
		if err = dbPool.Ping(); err == nil {
			log.Info("Postgres connection established", zap.String("dsn", loggedDSN), zap.Int("attempt", attempt))
			return dbPool, nil
		}
		log.Error("DB ping failed", zap.Error(err), zap.Int("attempt", attempt))
		time.Sleep(backoff)
		backoff *= 2
	}
	return nil, fmt.Errorf("db init failed after %d retries: %w", maxAttempts, err)
}

func maskDSN(dsn string) string {
	u, err := url.Parse(dsn)
	if err != nil {
		return dsn
	}
	if u.User != nil {
		_, hasPass := u.User.Password()
		if hasPass {
			u.User = url.UserPassword(u.User.Username(), "*****")
		}
	}
	return u.String()
}

func mustInitRedisClient(log *zap.Logger, cfg *config.StaticConfig) (*redis.Client, error) {
	opts := &redis.Options{
		Addr:         cfg.Redis.Addr,
		Password:     cfg.Redis.Password,
		DB:           cfg.Redis.DB,
		PoolSize:     cfg.Redis.PoolSize,
		MinIdleConns: cfg.Redis.MinIdleConnections,
		MaxRetries:   cfg.Redis.MaxRetries,
		ReadTimeout:  3 * time.Second,
		WriteTimeout: 3 * time.Second,
	}
	client := redis.NewClient(opts)
	backoff := 1 * time.Second
	var err error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		err = client.Ping(context.Background()).Err()
		if err == nil {
			log.Info("Redis connection established")
			return client, nil
		}
		log.Error("Redis ping failed", zap.Error(err), zap.Int("attempt", attempt))
		time.Sleep(backoff)
		backoff *= 2
		if backoff > 16*time.Second {
			backoff = 16 * time.Second
		}
	}
	return nil, fmt.Errorf("redis init failed after %d retries: %w", maxAttempts, err)
}
