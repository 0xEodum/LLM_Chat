package summary

import (
	"sync"
	"time"
)

// SummaryMetrics метрики для сервиса резюме
type SummaryMetrics struct {
	mu sync.RWMutex

	TotalSummariesCreated   int64
	TotalAnchorsCreated     int64
	TotalTokensUsed         int64
	TotalMessagesCompressed int64
	AverageSummaryTime      time.Duration

	summaryTimesSum time.Duration
	summaryCount    int64
}

func NewSummaryMetrics() *SummaryMetrics {
	return &SummaryMetrics{}
}

func (m *SummaryMetrics) RecordSummary(anchorsCount, tokensUsed, messagesCompressed int, duration time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.TotalSummariesCreated++
	m.TotalAnchorsCreated += int64(anchorsCount)
	m.TotalTokensUsed += int64(tokensUsed)
	m.TotalMessagesCompressed += int64(messagesCompressed)

	m.summaryTimesSum += duration
	m.summaryCount++
	m.AverageSummaryTime = m.summaryTimesSum / time.Duration(m.summaryCount)
}

func (m *SummaryMetrics) GetStats() (summaries, anchors, tokens, compressed int64, avgTime time.Duration) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	return m.TotalSummariesCreated, m.TotalAnchorsCreated, m.TotalTokensUsed,
		m.TotalMessagesCompressed, m.AverageSummaryTime
}
