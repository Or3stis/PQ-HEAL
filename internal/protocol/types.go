package protocol

import (
	"slices"
	"time"
)

// HeartbeatPayload is the shared heartbeat message type used by both client and server.
type HeartbeatPayload struct {
	ID        string     `json:"id"`
	Type      DataType   `json:"type"`
	Status    string     `json:"status,omitempty"`
	Timestamp *time.Time `json:"timestamp,omitempty"`
}

type DataType string

// Supported telemetry types shared by client and server.
const (
	DataHeartRate   DataType = "heart_rate"
	DataPersonTemp  DataType = "person_temperature"
	DataAmbientTemp DataType = "ambient_temperature"
	DataMotion      DataType = "motion"
)

var dataTypes = []DataType{
	DataHeartRate,
	DataPersonTemp,
	DataAmbientTemp,
	DataMotion,
}

// Types returns a copy so callers can't mutate the internal slice.
func Types() []DataType {
	out := make([]DataType, len(dataTypes))
	copy(out, dataTypes)
	return out
}

// IsValidType checks whether the value is one of the allowed telemetry types.
func IsValidType(t DataType) bool {
	return slices.Contains(dataTypes, t)
}

// UnitForType returns the fixed display unit for a data type.
func UnitForType(t DataType) string {
	switch t {
	case DataHeartRate:
		return "BPM"
	case DataPersonTemp, DataAmbientTemp:
		return "C"
	case DataMotion:
		return "Movement"
	default:
		return ""
	}
}

// Thresholds for alerting.
type Threshold struct {
	Min float64
	Max float64
}

var Thresholds = map[DataType]Threshold{
	DataHeartRate:  {Min: 40, Max: 120},
	DataPersonTemp: {Min: 35, Max: 38},
}

// DisplayNameForType returns a human-readable label for a data type.
func DisplayNameForType(t DataType) string {
	switch t {
	case DataHeartRate:
		return "Heart Rate"
	case DataPersonTemp:
		return "Person Temperature"
	case DataAmbientTemp:
		return "Ambient Temperature"
	case DataMotion:
		return "Motion"
	default:
		return string(t)
	}
}

// CheckThreshold returns true if the value is within normal range.
// Returns false (warning) if outside range.
func CheckThreshold(t DataType, val float64) bool {
	threshold, ok := Thresholds[t]
	if !ok {
		return true // No threshold defined
	}
	return val >= threshold.Min && val <= threshold.Max
}
