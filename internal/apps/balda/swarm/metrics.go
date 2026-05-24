package swarm

import "sync/atomic"

const (
	MetricShadowEnvelopesTotal      = "swarm_shadow_envelopes_total"
	MetricShadowDispatchTotal       = "swarm_shadow_dispatch_total"
	MetricShadowMissingSessionTotal = "swarm_shadow_missing_session_total"
	MetricShadowDedupeHitsTotal     = "swarm_shadow_dedupe_hits_total"
)

type ShadowMetrics struct {
	envelopes      atomic.Uint64
	dispatches     atomic.Uint64
	missingSession atomic.Uint64
	dedupeHits     atomic.Uint64
}

func NewShadowMetrics() *ShadowMetrics {
	return &ShadowMetrics{}
}

func (m *ShadowMetrics) RecordEnvelope() {
	if m != nil {
		m.envelopes.Add(1)
	}
}

func (m *ShadowMetrics) RecordDispatch() {
	if m != nil {
		m.dispatches.Add(1)
	}
}

func (m *ShadowMetrics) RecordMissingSession() {
	if m != nil {
		m.missingSession.Add(1)
	}
}

func (m *ShadowMetrics) RecordDedupeHit() {
	if m != nil {
		m.dedupeHits.Add(1)
	}
}

func (m *ShadowMetrics) Snapshot() map[string]uint64 {
	if m == nil {
		return map[string]uint64{
			MetricShadowEnvelopesTotal:      0,
			MetricShadowDispatchTotal:       0,
			MetricShadowMissingSessionTotal: 0,
			MetricShadowDedupeHitsTotal:     0,
		}
	}
	return map[string]uint64{
		MetricShadowEnvelopesTotal:      m.envelopes.Load(),
		MetricShadowDispatchTotal:       m.dispatches.Load(),
		MetricShadowMissingSessionTotal: m.missingSession.Load(),
		MetricShadowDedupeHitsTotal:     m.dedupeHits.Load(),
	}
}
