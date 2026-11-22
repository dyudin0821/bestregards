package app

import "testing"

func TestLoopbackOnly(t *testing.T) {
	for addr, want := range map[string]bool{
		"127.0.0.1:9090": true, "[::1]:9090": true, "localhost:9090": true,
		":9090": false, "0.0.0.0:9090": false, "[::]:9090": false, "10.0.0.5:9090": false, "metrics.internal:9090": false, "junk": false,
	} {
		if got := loopbackOnly(addr); got != want {
			t.Errorf("%q: %v, want %v", addr, got, want)
		}
	}
}
