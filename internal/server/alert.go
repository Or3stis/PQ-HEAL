package server

import (
	"log/slog"
	"sync/atomic"
	"time"

	"pq-heal/internal/protocol"
)

// AlertKind classifies why an alert was raised.
type AlertKind string

const (
	// AlertThresholdBreach is a reading outside its configured physiological
	// range. This is the protocol-level signal described in D2.2 Section 4.4.
	AlertThresholdBreach AlertKind = "threshold_breach"

	// AlertLivenessLoss is a client that has not been seen within its alive
	// window. It is a transition, emitted once when the client goes stale.
	AlertLivenessLoss AlertKind = "liveness_loss"

	// AlertAnomaly is a reading the anomaly detection model reported through
	// one of its adaptive or stream-integrity layers.
	AlertAnomaly AlertKind = "anomaly"
)

// Priority separates traffic that must reach an operator immediately from
// traffic that may be carried best-effort.
//
// The Home Hub classifies every alert, which is the hub-side half of the
// differentiated handling required by D1.1 Section 7.2. Binding a priority to a
// specific testbed network slice is deployment configuration and is completed
// in D3.1, once the slice management interface is available.
type Priority string

const (
	PriorityEmergency Priority = "emergency"
	PriorityRoutine   Priority = "routine"
)

// Alert is the structured event record published when the hub detects a
// condition an operator may need to act on. The field set is the one specified
// in D2.2 Section 4.4, extended with the detection layer, reason and score
// introduced by the anomaly detection model.
type Alert struct {
	Kind      AlertKind         `json:"kind"`
	Priority  Priority          `json:"priority"`
	ClientID  string            `json:"client_id"`
	Type      protocol.DataType `json:"type"`
	At        time.Time         `json:"at"`
	RequestID string            `json:"request_id,omitempty"`

	// Value and Unit describe the observed reading. They are unset for
	// liveness-loss alerts, which are triggered by absence rather than by data;
	// HasValue is what distinguishes those, so Value is emitted even when zero.
	// A reported zero is meaningful here — a heart rate of 0 is a breach.
	Value    float64 `json:"value"`
	Unit     string  `json:"unit,omitempty"`
	HasValue bool    `json:"has_value"`

	// ThresholdMin and ThresholdMax carry the range that was breached and are
	// set only for AlertThresholdBreach.
	ThresholdMin float64 `json:"threshold_min,omitempty"`
	ThresholdMax float64 `json:"threshold_max,omitempty"`

	// LastSeen is the final observation of a client that has gone stale and is
	// set only for AlertLivenessLoss.
	LastSeen time.Time `json:"last_seen,omitzero"`

	// Layer, Reason and Score explain an anomaly verdict. Score is populated
	// only when the Isolation Forest was consulted.
	Layer  string  `json:"layer,omitempty"`
	Reason string  `json:"reason,omitempty"`
	Score  float64 `json:"score,omitempty"`
}

// AlertSink receives published alerts. Implementations must not block: the
// publishing path runs inside the detection pipeline, and a slow sink would
// apply backpressure to telemetry processing.
type AlertSink interface {
	Publish(Alert)
}

// SinkFunc adapts a function to AlertSink.
type SinkFunc func(Alert)

func (f SinkFunc) Publish(a Alert) { f(a) }

// LogSink writes alerts to the structured logger. It is the default sink and
// is what makes alerts visible in the container logs of an edge deployment.
type LogSink struct{}

func (LogSink) Publish(a Alert) {
	attrs := []any{
		"kind", string(a.Kind),
		"priority", string(a.Priority),
		"client_id", a.ClientID,
		"type", string(a.Type),
	}
	if a.HasValue {
		attrs = append(attrs, "value", a.Value, "unit", a.Unit)
	}
	if a.Kind == AlertThresholdBreach {
		attrs = append(attrs, "threshold_min", a.ThresholdMin, "threshold_max", a.ThresholdMax)
	}
	if a.Kind == AlertLivenessLoss {
		attrs = append(attrs, "last_seen", a.LastSeen.Format(time.RFC3339Nano))
	}
	if a.Layer != "" {
		attrs = append(attrs, "layer", a.Layer, "reason", a.Reason)
	}
	if a.Score > 0 {
		attrs = append(attrs, "score", a.Score)
	}
	if a.RequestID != "" {
		attrs = append(attrs, "request_id", a.RequestID)
	}
	slog.Warn("alert", attrs...)
}

// AlertBus fans alerts out to a bounded channel that operator-facing components
// consume. D2.2 Section 4.4 specifies this decoupling: the detecting layer only
// publishes, leaving correlation and clinical escalation to its consumers.
//
// Publication is non-blocking. If no consumer is draining the channel the
// alert is dropped and counted rather than stalling telemetry processing,
// because a monitoring system that stops ingesting under alert load has failed
// in a more serious way than one that loses a duplicate notification.
type AlertBus struct {
	ch      chan Alert
	dropped atomic.Uint64
}

// DefaultAlertBuffer is the number of alerts held for a slow consumer.
const DefaultAlertBuffer = 256

func NewAlertBus(buffer int) *AlertBus {
	if buffer <= 0 {
		buffer = DefaultAlertBuffer
	}
	return &AlertBus{ch: make(chan Alert, buffer)}
}

func (b *AlertBus) Publish(a Alert) {
	select {
	case b.ch <- a:
	default:
		b.dropped.Add(1)
	}
}

// Alerts returns the receive side of the bus.
func (b *AlertBus) Alerts() <-chan Alert { return b.ch }

// Dropped reports how many alerts were discarded because no consumer kept up.
// A non-zero value is an operational signal and is surfaced at shutdown.
func (b *AlertBus) Dropped() uint64 { return b.dropped.Load() }

// MultiSink publishes to several sinks in order.
type MultiSink []AlertSink

func (m MultiSink) Publish(a Alert) {
	for _, s := range m {
		s.Publish(a)
	}
}

// classifyPriority assigns transport priority. Readings that describe the
// monitored subject are treated as emergencies; environmental readings and
// device-integrity conditions are routine, since neither implies immediate
// clinical risk on its own.
func classifyPriority(kind AlertKind, dataType protocol.DataType) Priority {
	switch dataType {
	case protocol.DataHeartRate, protocol.DataPersonTemp:
		if kind == AlertThresholdBreach || kind == AlertAnomaly {
			return PriorityEmergency
		}
	}
	if kind == AlertLivenessLoss {
		// A silent device may mean a fall or a removed sensor, so liveness loss
		// on a vital-sign stream is escalated even though no value accompanies it.
		switch dataType {
		case protocol.DataHeartRate, protocol.DataPersonTemp:
			return PriorityEmergency
		}
	}
	return PriorityRoutine
}

// thresholdAlert builds a breach record for a reading outside its range.
func thresholdAlert(clientID string, dataType protocol.DataType, value float64, unit string, at time.Time, requestID string) Alert {
	a := Alert{
		Kind:      AlertThresholdBreach,
		Priority:  classifyPriority(AlertThresholdBreach, dataType),
		ClientID:  clientID,
		Type:      dataType,
		At:        at,
		RequestID: requestID,
		Value:     value,
		Unit:      unit,
		HasValue:  true,
		Reason:    "reading outside the configured physiological range",
	}
	if th, ok := protocol.Thresholds[dataType]; ok {
		a.ThresholdMin = th.Min
		a.ThresholdMax = th.Max
	}
	return a
}

// livenessAlert builds a record for a client that has stopped reporting.
func livenessAlert(rec ClientRecord, at time.Time) Alert {
	return Alert{
		Kind:     AlertLivenessLoss,
		Priority: classifyPriority(AlertLivenessLoss, rec.Type),
		ClientID: rec.ID,
		Type:     rec.Type,
		At:       at,
		LastSeen: rec.LastSeen,
		Reason:   "client not seen within its alive window",
	}
}
