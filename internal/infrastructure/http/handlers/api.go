// Package handlers defines API endpoints for the application using Gin.
package handlers

import (
	"MessageDispatcher/internal/app/sender"
	"MessageDispatcher/internal/infrastructure/scheduler"
	"context"
	"net/http"

	"github.com/gin-gonic/gin"
)

// StartHandler
// @Summary Start automatic message sending
// @Description Starts the scheduler if not running.
// @Accept json
// @Produce json
// @Success 200 {string} string "Started"
// @Router /start [post]
func StartHandler(scheduler *scheduler.Scheduler) gin.HandlerFunc {
	return func(c *gin.Context) {
		scheduler.Start(context.Background(), true)
		c.JSON(http.StatusOK, "Started")
	}
}

// StopHandler
// @Summary Stop automatic message sending
// @Description Stops the scheduler if running.
// @Accept json
// @Produce json
// @Success 200 {string} string "Stopped"
// @Router /stop [post]
func StopHandler(scheduler *scheduler.Scheduler) gin.HandlerFunc {
	return func(c *gin.Context) {
		scheduler.Stop()
		c.JSON(http.StatusOK, "Stopped")
	}
}

// GetSentHandler
// @Summary Retrieve list of sent messages
// @Description Gets all sent messages from the repository via service.
// @Produce json
// @Success 200 {array} entities.Message
// @Failure 500 {object} string "Internal Server Error"
// @Router /sent [get]
func GetSentHandler(service *sender.Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		sentMessages, err := service.GetSentMessages(c.Request.Context())
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, sentMessages)
	}
}
