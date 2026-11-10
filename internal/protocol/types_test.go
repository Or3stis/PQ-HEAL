package protocol

import (
	"fmt"
	"testing"
)

func TestUnitForType(t *testing.T) {
	cases := map[DataType]string{
		DataHeartRate:   "BPM",
		DataPersonTemp:  "C",
		DataAmbientTemp: "C",
		DataMotion:      "Movement",
	}

	for typ, expected := range cases {
		if got := UnitForType(typ); got != expected {
			t.Fatalf("expected %s for %s, got %s", expected, typ, got)
		}
	}

	if UnitForType("unknown") != "" {
		t.Fatalf("expected empty unit for unknown type")
	}
}

func TestCheckThreshold(t *testing.T) {
	cases := []struct {
		typ      DataType
		val      float64
		expected bool
	}{
		{DataHeartRate, 70, true},
		{DataHeartRate, 30, false},
		{DataHeartRate, 130, false},
		{DataPersonTemp, 37, true},
		{DataPersonTemp, 34, false},
		{DataPersonTemp, 39, false},
		{DataAmbientTemp, 25, true}, // No threshold defined for ambient temp, should be true
	}

	for _, c := range cases {
		if got := CheckThreshold(c.typ, c.val); got != c.expected {
			t.Errorf("CheckThreshold(%s, %f) = %v; want %v", c.typ, c.val, got, c.expected)
		}
	}
}

func TestCheckThresholdBoundaries(t *testing.T) {
	cases := []struct {
		typ  DataType
		val  float64
		want bool
		desc string
	}{
		// HeartRate: min=40, max=120 (inclusive on both ends)
		{DataHeartRate, 40, true, "at min"},
		{DataHeartRate, 120, true, "at max"},
		{DataHeartRate, 39.9, false, "below min"},
		{DataHeartRate, 120.1, false, "above max"},
		// PersonTemp: min=35, max=38 (inclusive on both ends)
		{DataPersonTemp, 35, true, "at min"},
		{DataPersonTemp, 38, true, "at max"},
		{DataPersonTemp, 34.9, false, "below min"},
		{DataPersonTemp, 38.1, false, "above max"},
		// Types with no defined threshold always return true.
		{DataAmbientTemp, -9999, true, "no threshold"},
		{DataMotion, 9999, true, "no threshold"},
	}
	for _, c := range cases {
		t.Run(fmt.Sprintf("%s/%s", c.typ, c.desc), func(t *testing.T) {
			if got := CheckThreshold(c.typ, c.val); got != c.want {
				t.Errorf("CheckThreshold(%s, %g) = %v; want %v", c.typ, c.val, got, c.want)
			}
		})
	}
}
