package config

import "testing"

func TestTaskWindows(t *testing.T) {
	for _, tc := range []struct {
		c             *TaskConfig
		start, finish float64
	}{
		{nil, 900, 86400}, {&TaskConfig{}, 900, 86400},
		{&TaskConfig{StartSeconds: 1200, FinishSeconds: 7200}, 1200, 7200},
		{&TaskConfig{StartSeconds: -1, FinishSeconds: -1}, 900, 86400},
	} {
		start, finish := tc.c.Windows()
		if start != tc.start || finish != tc.finish {
			t.Fatalf("windows = %v,%v; want %v,%v", start, finish, tc.start, tc.finish)
		}
	}
}
