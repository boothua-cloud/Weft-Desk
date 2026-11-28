package engine

import "testing"

func TestRelayLabel(t *testing.T) {
	if got := RelayLabel("headscale"); got != "自建中继" {
		t.Fatalf("headscale 内嵌中继不该原样显示: %q", got)
	}
	if got := RelayLabel("hkg"); got != "hkg" {
		t.Fatalf("公网区域照原样: %q", got)
	}
}
