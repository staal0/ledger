package app

import (
	"net/http/httptest"
	"testing"
)

func TestYearRange(t *testing.T) {
	var fifty []int // 1990 … 2039
	for y := 1990; y < 2040; y++ {
		fifty = append(fifty, y)
	}
	four := []int{2023, 2024, 2025, 2026}
	six := []int{2021, 2022, 2023, 2024, 2025, 2026}
	tests := []struct {
		query          string
		all            []int
		wantFrom, want int
	}{
		{"", four, 2023, 2026},          // fewer than five years: all of them
		{"", fifty, 2035, 2039},         // default: the newest five
		{"?to=2000", fifty, 1996, 2000}, // five years up to "to"
		{"?from=1995&to=2005", fifty, 1995, 2005},
		{"?from=2005&to=1995", fifty, 1995, 2005}, // reversed is swapped
		{"?from=1800&to=2100", six, 2021, 2026},   // clamped to existing years
		{"?from=abc", four, 2023, 2026},           // garbage is ignored
		{"", nil, 0, 0},
	}
	for _, tt := range tests {
		from, to := yearRange(httptest.NewRequest("GET", "/stats"+tt.query, nil), tt.all, 5)
		if from != tt.wantFrom || to != tt.want {
			t.Errorf("yearRange(%q, %d years) = %d–%d, want %d–%d", tt.query, len(tt.all), from, to, tt.wantFrom, tt.want)
		}
	}
}
