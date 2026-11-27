package main

import (
	"testing"
	"time"
)

func TestFormatUptime(t *testing.T) {
	cases := []struct {
		d time.Duration
		w string
	}{
		{0, "0 秒"},
		{9 * time.Second, "9 秒"},
		{90 * time.Second, "1 分 30 秒"},
		{42 * time.Minute, "42 分 0 秒"},
		{3*time.Hour + 12*time.Minute, "3 小时 12 分"},
		{5 * time.Hour, "5 小时"},
		{50*time.Hour + 3*time.Minute, "2 天 2 小时"},
		{-5 * time.Second, "0 秒"},
	}
	for _, c := range cases {
		if got := formatUptime(c.d); got != c.w {
			t.Fatalf("formatUptime(%v) = %q, want %q", c.d, got, c.w)
		}
	}
}
