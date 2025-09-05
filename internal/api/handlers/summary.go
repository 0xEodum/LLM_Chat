package handlers

import (
	"net/http"

	"LLM_Chat/internal/service/summary"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

type SummaryHandler struct {
	summaryService summary.SummaryService
	logger         *zap.Logger
}

func NewSummaryHandler(
	summaryService summary.SummaryService,
	logger *zap.Logger,
) *SummaryHandler {
	return &SummaryHandler{
		summaryService: summaryService,
		logger:         logger,
	}
}

// GET /chat/:session_id/summary - получение резюме сессии
func (h *SummaryHandler) GetSummary(c *gin.Context) {
	sessionID := c.Param("session_id")
	if sessionID == "" {
		c.JSON(http.StatusBadRequest, ErrorResponse{
			Error: "session_id is required",
			Code:  "MISSING_SESSION_ID",
		})
		return
	}

	summary, err := h.summaryService.GetSummary(c.Request.Context(), sessionID)
	if err != nil {
		h.logger.Error("Failed to get summary",
			zap.Error(err),
			zap.String("session_id", sessionID),
		)
		c.JSON(http.StatusNotFound, ErrorResponse{
			Error:   "Summary not found",
			Code:    "SUMMARY_NOT_FOUND",
			Details: err.Error(),
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"session_id": sessionID,
		"summary":    summary,
	})
}

// DELETE /chat/:session_id/summary - удаление резюме
func (h *SummaryHandler) DeleteSummary(c *gin.Context) {
	sessionID := c.Param("session_id")
	if sessionID == "" {
		c.JSON(http.StatusBadRequest, ErrorResponse{
			Error: "session_id is required",
			Code:  "MISSING_SESSION_ID",
		})
		return
	}

	if err := h.summaryService.DeleteSummary(c.Request.Context(), sessionID); err != nil {
		h.logger.Error("Failed to delete summary",
			zap.Error(err),
			zap.String("session_id", sessionID),
		)
		c.JSON(http.StatusInternalServerError, ErrorResponse{
			Error:   "Failed to delete summary",
			Code:    "DELETE_SUMMARY_ERROR",
			Details: err.Error(),
		})
		return
	}

	h.logger.Info("Summary deleted", zap.String("session_id", sessionID))
	c.JSON(http.StatusOK, gin.H{
		"message":    "Summary deleted successfully",
		"session_id": sessionID,
	})
}
