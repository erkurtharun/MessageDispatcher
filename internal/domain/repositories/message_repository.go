package repositories

import (
	"MessageDispatcher/internal/domain/entities"
	"context"
	"time"
)

type MessageRepository interface {
	GetUnsentMessages(ctx context.Context, limit int) ([]entities.Message, error)

	MarkAsSent(ctx context.Context, id int, sentAt time.Time) error

	GetSentMessages(ctx context.Context) ([]entities.Message, error)
}
