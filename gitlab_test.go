package main

import (
	"testing"
	"time"
)

func TestResolveDateRange(t *testing.T) {
	tests := []struct {
		duration string
		wantErr  bool
	}{
		{"1d", false},
		{"3d", false},
		{"7d", false},
		{"1m", false},
		{"3m", false},
		{"12m", false},
		{"2026-06-01", false},
		{"2026-06-01:2026-06-05", false},
		{"invalid", true},
		{"2026-06-01:invalid", true},
		{"invalid:2026-06-05", true},
	}

	for _, tt := range tests {
		since, until, err := ResolveDateRange(tt.duration)
		if (err != nil) != tt.wantErr {
			t.Errorf("ResolveDateRange(%q) error = %v, wantErr %v", tt.duration, err, tt.wantErr)
			continue
		}
		if err == nil {
			if since.After(until) {
				t.Errorf("ResolveDateRange(%q) since %v is after until %v", tt.duration, since, until)
			}
		}
	}
}

func TestResolveDateRangeSpecific(t *testing.T) {
	// Test relative date parsing
	since, _, err := ResolveDateRange("1d")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	expectedSince := time.Now().AddDate(0, 0, -1)
	// Compare dates ignoring seconds/milliseconds due to small delay
	if since.Format("2006-01-02 15:04") != expectedSince.Format("2006-01-02 15:04") {
		t.Errorf("expected since close to %v, got %v", expectedSince, since)
	}

	// Test custom date range
	since, until, err := ResolveDateRange("2026-06-01:2026-06-05")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if since.Format("2006-01-02") != "2026-06-01" {
		t.Errorf("expected since to be 2026-06-01, got %v", since.Format("2006-01-02"))
	}
	if until.Format("2006-01-02") != "2026-06-05" {
		t.Errorf("expected until to be 2026-06-05, got %v", until.Format("2006-01-02"))
	}
}
