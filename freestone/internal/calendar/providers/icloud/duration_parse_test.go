package icloud

import (
	"testing"
	"time"
)

func TestParseDuration(t *testing.T) {
	for _, tc := range []struct {
		value string
		want  eventSpan
	}{
		{"PT1H", eventSpan{exact: time.Hour}},
		{"+PT15M", eventSpan{exact: 15 * time.Minute}},
		{"PT45S", eventSpan{exact: 45 * time.Second}},
		{"PT1H30M15S", eventSpan{exact: time.Hour + 30*time.Minute + 15*time.Second}},
		{"P1D", eventSpan{days: 1}},
		{"P2DT3H", eventSpan{days: 2, exact: 3 * time.Hour}},
		{"P2W", eventSpan{days: 14}},
		{"pt1h", eventSpan{exact: time.Hour}},
		{"P0D", eventSpan{}},
	} {
		got, err := parseDuration(tc.value)
		if err != nil {
			t.Errorf("parseDuration(%q): %v", tc.value, err)
			continue
		}
		if got != tc.want {
			t.Errorf("parseDuration(%q) = %+v, want %+v", tc.value, got, tc.want)
		}
	}

	for _, bad := range []string{"", "P", "PT", "1H", "-PT1H", "P1W2D", "PT1X", "P1DT", "PT1M1H", "P1H", "PTH"} {
		if got, err := parseDuration(bad); err == nil {
			t.Errorf("parseDuration(%q) = %+v, want an error", bad, got)
		}
	}
}
