package repository

import (
	"errors"
	"testing"
	"time"
)

// Regression guards for the P0 silent corruption fixes in conv.go.
// formatRFC used to substitute time.Now() for zero time; parseRFC used to
// swallow time.Parse errors. Both produced silent "valid-looking" garbage that
// propagated through the bitemporal write path.

func TestFormatRFC_RejectsZeroTime(t *testing.T) {
	_, err := formatRFC(time.Time{})
	if !errors.Is(err, ErrZeroTime) {
		t.Fatalf("expected ErrZeroTime, got %v", err)
	}
}

func TestFormatRFC_FormatsNonZeroAsUTC(t *testing.T) {
	loc, _ := time.LoadLocation("Asia/Taipei")
	t1 := time.Date(2026, 6, 8, 14, 30, 0, 0, loc)
	got, err := formatRFC(t1)
	if err != nil {
		t.Fatalf("formatRFC: %v", err)
	}
	want := "2026-06-08T06:30:00Z"
	if got != want {
		t.Errorf("UTC conversion: got %q want %q", got, want)
	}
}

func TestParseRFC_RejectsEmptyString(t *testing.T) {
	_, err := parseRFC("")
	if err == nil {
		t.Fatal("expected error for empty string")
	}
}

func TestParseRFC_PropagatesParseError(t *testing.T) {
	_, err := parseRFC("not a timestamp")
	if err == nil {
		t.Fatal("expected error for malformed input")
	}
}

func TestParseRFC_ValidInput(t *testing.T) {
	got, err := parseRFC("2026-06-08T06:30:00Z")
	if err != nil {
		t.Fatalf("parseRFC: %v", err)
	}
	want := time.Date(2026, 6, 8, 6, 30, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("got %v want %v", got, want)
	}
}
