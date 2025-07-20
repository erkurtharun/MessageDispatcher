// Package db Injects the DB pool for shared connection management,
// uses context for cancellation, row locking for concurrency, and error wrapping for diagnostics.
package db

import (
	"MessageDispatcher/internal/domain/entities"
	"MessageDispatcher/internal/domain/repositories"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	_ "github.com/lib/pq"
)

var (
	ErrNoMessageFound          = errors.New("no message found")
	ErrDatabaseOperationFailed = errors.New("database operation failed")
)

type postgresRepo struct {
	db *sql.DB
}

func NewPostgresRepo(db *sql.DB) repositories.MessageRepository {
	return &postgresRepo{db: db}
}

func (r *postgresRepo) GetUnsentMessages(ctx context.Context, limit int) ([]entities.Message, error) {
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return nil, fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer func() {
		if err != nil {
			if rollbackErr := tx.Rollback(); rollbackErr != nil {
				err = errors.Join(err, fmt.Errorf("failed to rollback transaction: %w", rollbackErr))
			}
		} else {
			if commitErr := tx.Commit(); commitErr != nil {
				err = fmt.Errorf("failed to commit transaction: %w", commitErr)
			}
		}
	}()

	query := `SELECT id, recipient_phone, content 
	          FROM messages 
	          WHERE sent_status = FALSE 
	          LIMIT $1 
	          FOR UPDATE SKIP LOCKED`
	rows, err := tx.QueryContext(ctx, query, limit)
	if err != nil {
		return nil, fmt.Errorf("%w: failed to query unsent messages: %v", ErrDatabaseOperationFailed, err)
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("failed to close rows for unsent messages: %w", closeErr))
		}
	}()

	var unsentMessages []entities.Message
	for rows.Next() {
		var msg entities.Message
		if err := rows.Scan(&msg.ID, &msg.RecipientPhone, &msg.Content); err != nil {
			return nil, fmt.Errorf("failed to scan unsent message: %w", err)
		}
		unsentMessages = append(unsentMessages, msg)
	}
	if scanErr := rows.Err(); scanErr != nil {
		return nil, fmt.Errorf("error iterating unsent messages: %w", scanErr)
	}
	return unsentMessages, nil
}

func (r *postgresRepo) MarkAsSent(ctx context.Context, id int, sentAt time.Time) error {
	sentAt = sentAt.UTC()

	query := `UPDATE messages 
	          SET sent_status = TRUE, sent_at = $1 
	          WHERE id = $2`
	result, err := r.db.ExecContext(ctx, query, sentAt, id)
	if err != nil {
		return fmt.Errorf("%w: failed to mark message as sent: %v", ErrDatabaseOperationFailed, err)
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("failed to check rows affected: %w", err)
	}
	if rowsAffected == 0 {
		return fmt.Errorf("%w: with id %d", ErrNoMessageFound, id)
	}
	return nil
}

func (r *postgresRepo) GetSentMessages(ctx context.Context) ([]entities.Message, error) {
	query := `SELECT id, recipient_phone, content, sent_at 
	          FROM messages 
	          WHERE sent_status = TRUE
	          ORDER BY sent_at DESC`
	rows, err := r.db.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("%w: failed to query sent messages: %v", ErrDatabaseOperationFailed, err)
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("failed to close rows for sent messages: %w", closeErr))
		}
	}()

	var sentMessages []entities.Message
	for rows.Next() {
		var msg entities.Message
		var sentAt sql.NullTime
		if err := rows.Scan(&msg.ID, &msg.RecipientPhone, &msg.Content, &sentAt); err != nil {
			return nil, fmt.Errorf("failed to scan sent message: %w", err)
		}
		if sentAt.Valid {
			msg.SentAt = sentAt.Time.UTC()
		}
		msg.SentStatus = true
		sentMessages = append(sentMessages, msg)
	}
	if scanErr := rows.Err(); scanErr != nil {
		return nil, fmt.Errorf("error iterating sent messages: %w", scanErr)
	}
	return sentMessages, nil
}
