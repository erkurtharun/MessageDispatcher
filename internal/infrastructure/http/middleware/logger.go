// Package middleware adds correlation ID (generates if missing, propagates in headers),
// stores it in context for request scoping, and logs request details at completion.
// Best practices: Uses UUID for ID, ECS-compatible fields (trace.id)
package middleware

import (
	"MessageDispatcher/internal/pkg/logger"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

const CorrelationIDKey = "corr_id"

func LoggerMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		corrID := c.GetHeader("X-Correlation-ID")
		if corrID == "" {
			corrID = uuid.NewString()
		}

		c.Writer.Header().Set("X-Correlation-ID", corrID)

		c.Set(CorrelationIDKey, corrID)

		c.Next()

		log := logger.WithCorrID(corrID)
		log.Info("Request handled",
			zap.String("method", c.Request.Method),
			zap.String("path", c.Request.URL.Path),
			zap.Int("status", c.Writer.Status()),
			zap.Int("size", c.Writer.Size()),
			zap.String("user_agent", c.Request.UserAgent()),
		)
	}
}
