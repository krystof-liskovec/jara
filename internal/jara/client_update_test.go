package jara

import "testing"

func TestVersionNewer(t *testing.T) {
	for _, test := range []struct {
		candidate string
		current   string
		want      bool
	}{
		{"1.2.0", "1.1.9", true},
		{"1.2.0", "1.2.0-dev", true},
		{"1.2.0-dev", "1.2.0", false},
		{"1.2.0", "1.2.0", false},
		{"dev", "1.2.0", false},
	} {
		if got := versionNewer(test.candidate, test.current); got != test.want {
			t.Errorf("versionNewer(%q, %q) = %t, want %t", test.candidate, test.current, got, test.want)
		}
	}
}
