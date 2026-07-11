package observability

import (
	"runtime"
	"sync/atomic"
	"time"
)

// MetricsCollector holds all in-process counters. All fields are safe for concurrent use.
type MetricsCollector struct {
	// Inbound: Evolution → Chatwoot
	InboundTotal   atomic.Int64
	InboundSuccess atomic.Int64
	InboundError   atomic.Int64

	// Outbound: Chatwoot → WhatsApp
	OutboundTotal   atomic.Int64
	OutboundSuccess atomic.Int64
	OutboundError   atomic.Int64

	// Auth failures
	Error401Count atomic.Int64

	// Lookup failures (tenant/contact not found)
	Error404Count atomic.Int64

	startedAt time.Time
}

func New() *MetricsCollector {
	return &MetricsCollector{startedAt: time.Now()}
}

func (m *MetricsCollector) UptimeSeconds() int64 {
	return int64(time.Since(m.startedAt).Seconds())
}

type RuntimeSnapshot struct {
	Goroutines  int
	AllocMB     float64
	SysMB       float64
	UptimeSeconds int64
}

func (m *MetricsCollector) RuntimeSnapshot() RuntimeSnapshot {
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	return RuntimeSnapshot{
		Goroutines:    runtime.NumGoroutine(),
		AllocMB:       float64(ms.Alloc) / 1024 / 1024,
		SysMB:         float64(ms.Sys) / 1024 / 1024,
		UptimeSeconds: m.UptimeSeconds(),
	}
}

func errorRate(total, errors int64) float64 {
	if total == 0 {
		return 0
	}
	return float64(errors) / float64(total) * 100
}

type TrafficSnapshot struct {
	InboundTotal      int64
	InboundSuccess    int64
	InboundError      int64
	InboundErrorRate  float64
	OutboundTotal     int64
	OutboundSuccess   int64
	OutboundError     int64
	OutboundErrorRate float64
	Error401Count     int64
	Error404Count     int64
}

func (m *MetricsCollector) TrafficSnapshot() TrafficSnapshot {
	inTotal := m.InboundTotal.Load()
	inErr := m.InboundError.Load()
	outTotal := m.OutboundTotal.Load()
	outErr := m.OutboundError.Load()
	return TrafficSnapshot{
		InboundTotal:      inTotal,
		InboundSuccess:    m.InboundSuccess.Load(),
		InboundError:      inErr,
		InboundErrorRate:  errorRate(inTotal, inErr),
		OutboundTotal:     outTotal,
		OutboundSuccess:   m.OutboundSuccess.Load(),
		OutboundError:     outErr,
		OutboundErrorRate: errorRate(outTotal, outErr),
		Error401Count:     m.Error401Count.Load(),
		Error404Count:     m.Error404Count.Load(),
	}
}
