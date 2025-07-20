package migration

import (
	"context"
	"database/sql"
	"embed"
	"fmt"

	"github.com/pressly/goose/v3"
	"go.uber.org/zap"
)

//go:embed migrations/*
var migrations embed.FS

type gooseZapLogger struct {
	log *zap.Logger
}

func (l *gooseZapLogger) Printf(format string, args ...any) {
	l.log.Info(fmt.Sprintf(format, args...))
}

func (l *gooseZapLogger) Fatalf(format string, args ...any) {
	l.log.Error(fmt.Sprintf(format, args...))
}

func Migrate(ctx context.Context, db *sql.DB, env string, log *zap.Logger) error {
	if env != "local" && env != "dev" {
		log.Info("Migrations skipped in non-dev environment")
		return nil
	}

	goose.SetBaseFS(migrations)

	goose.SetLogger(&gooseZapLogger{log: log.With(zap.String("component", "goose_migration"))})

	if err := goose.SetDialect("postgres"); err != nil {
		return fmt.Errorf("failed to set dialect: %w", err)
	}

	if err := goose.UpContext(ctx, db, "migrations"); err != nil {
		return fmt.Errorf("migration failed: %w", err)
	}

	log.Info("Migrations applied successfully")
	return nil
}
