// Package sender contains the application service for message sending use cases.
package sender

import (
	"MessageDispatcher/internal/domain/entities"
	"MessageDispatcher/internal/domain/repositories"
	"MessageDispatcher/internal/infrastructure/cache"
	"MessageDispatcher/internal/pkg/dynamicconfig"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"go.uber.org/zap"
	"net/http"
	"time"
)

type Service struct {
	repo          repositories.MessageRepository
	cache         *cache.RedisCache
	httpClient    *http.Client
	webhookURL    string
	configManager *dynamicconfig.Manager
	logger        *zap.Logger
}

type ServiceDeps struct {
	Repo          repositories.MessageRepository
	Cache         *cache.RedisCache
	WebhookURL    string
	ConfigManager *dynamicconfig.Manager
	Logger        *zap.Logger
}

func NewService(deps ServiceDeps) *Service {
	return &Service{
		repo:          deps.Repo,
		cache:         deps.Cache,
		httpClient:    &http.Client{Timeout: 10 * time.Second},
		webhookURL:    deps.WebhookURL,
		configManager: deps.ConfigManager,
		logger:        deps.Logger,
	}
}

func (s *Service) SendUnsentMessages(ctx context.Context) error {
	config := s.configManager.Get()
	if !config.AutoSending {
		s.logger.Info("Auto-sending disabled by toggle")
		return nil
	}

	unsentMessages, err := s.repo.GetUnsentMessages(ctx, config.RateLimit)
	if err != nil {
		s.logger.Error("Failed to get unsent messages", zap.Error(err))
		return fmt.Errorf("get unsent failed: %w", err)
	}

	for _, msg := range unsentMessages {
		if err := msg.Validate(); err != nil {
			s.logger.Warn("Invalid message skipped", zap.Int("id", msg.ID), zap.Error(err))
			continue
		}

		payload := map[string]string{
			"to":      msg.RecipientPhone,
			"content": msg.Content,
		}
		body, _ := json.Marshal(payload)
		req, err := http.NewRequestWithContext(ctx, "POST", s.webhookURL, bytes.NewBuffer(body))
		if err != nil {
			s.logger.Error("Webhook request creation failed", zap.Int("id", msg.ID), zap.Error(err))
			continue
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("x-ins-auth-key", config.WebhookAuthKey)

		resp, err := s.httpClient.Do(req)
		if err != nil {
			s.logger.Error("Webhook send failed", zap.Int("id", msg.ID), zap.Error(err))
			continue
		}
		func() {
			defer func() {
				if err := resp.Body.Close(); err != nil {
					s.logger.Warn("Redis client close failed", zap.Error(err))
				}
			}()

			if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
				s.logger.Warn("Webhook non-2xx response", zap.Int("id", msg.ID), zap.Int("status", resp.StatusCode))
				return
			}

			var res struct {
				Message   string `json:"message"`
				MessageID string `json:"messageId"`
			}
			if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
				s.logger.Error("Webhook response decode failed", zap.Int("id", msg.ID), zap.Error(err))
				return
			}

			now := time.Now()
			if err := s.repo.MarkAsSent(ctx, msg.ID, now); err != nil {
				s.logger.Error("Mark as sent failed", zap.Int("id", msg.ID), zap.Error(err))
				return
			}

			if err := s.cache.CacheSentMessage(ctx, res.MessageID, now); err != nil {
				s.logger.Warn("Cache sent message failed", zap.String("message_id", res.MessageID), zap.Error(err))
			}

			s.logger.Info("Message sent successfully", zap.Int("id", msg.ID), zap.String("message_id", res.MessageID))
		}()
	}
	return nil
}

func (s *Service) GetSentMessages(ctx context.Context) ([]entities.Message, error) {
	sentMessages, err := s.repo.GetSentMessages(ctx)
	if err != nil {
		s.logger.Error("Failed to get sent messages", zap.Error(err))
		return nil, fmt.Errorf("get sent failed: %w", err)
	}
	return sentMessages, nil
}
