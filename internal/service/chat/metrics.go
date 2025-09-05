package chat

import (
	"sync"
	"time"

	"go.uber.org/zap"
)

// SimpleMetrics простая реализация метрик для мониторинга
type SimpleMetrics struct {
	mu sync.RWMutex

	TotalMessages       int64
	TotalTokens         int64
	TotalCost           float64
	AverageResponseTime time.Duration

	responseTimesSum time.Duration
	responseCount    int64
}

func NewSimpleMetrics() *SimpleMetrics {
	return &SimpleMetrics{}
}

func (m *SimpleMetrics) RecordMessage(tokens int, cost float64, responseTime time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.TotalMessages++
	m.TotalTokens += int64(tokens)
	m.TotalCost += cost

	m.responseTimesSum += responseTime
	m.responseCount++
	m.AverageResponseTime = m.responseTimesSum / time.Duration(m.responseCount)
}

func (m *SimpleMetrics) GetStats() (messages, tokens int64, cost float64, avgTime time.Duration) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	return m.TotalMessages, m.TotalTokens, m.TotalCost, m.AverageResponseTime
}

func (s *Service) recordMetrics(tokens int, cost float64, responseTime time.Duration) {
	// TODO: Интегрировать с реальной системой метрик (Prometheus, etc.)
	s.logger.Info("Message metrics",
		zap.Int("tokens", tokens),
		zap.Float64("cost", cost),
		zap.Duration("response_time", responseTime),
	)
}
