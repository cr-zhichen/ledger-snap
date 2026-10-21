package server

import "testing"

func TestGoesBack(t *testing.T) {
	cases := []struct {
		step, value string
		want        bool
	}{
		{"asset", optBack, true},
		{"asset", "", false},
		{"money", "", true},
		{"money", "0", true},
		{"money", "0.00", true},
		{"money", "12.5", false},
		{"money", "1,200", false},
		{"other", "0", true},
		{"time", "", true},
		{"time", "0", false},
		{"text", "", true},
		{"text", "改成打车", false},
		{"main", "", false},
		{"edit", "", false},
	}
	for _, tc := range cases {
		if got := goesBack(tc.step, tc.value); got != tc.want {
			t.Errorf("goesBack(%q, %q) = %v", tc.step, tc.value, got)
		}
	}
}
