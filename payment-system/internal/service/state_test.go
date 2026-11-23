package service

import "testing"

func TestCanTransition(t *testing.T) {
	// 表驱动覆盖允许、终态同态重放和跨终态非法转换。
	tests := []struct {
		from, to string
		want     bool
	}{
		{"pending", "success", true},
		{"pending", "failed", true},
		{"pending", "closed", true},
		{"pending", "pending", true},
		{"success", "success", true},
		{"failed", "success", false},
		{"closed", "success", false},
		{"success", "closed", false},
	}
	for _, tt := range tests {
		t.Run(tt.from+"_"+tt.to, func(t *testing.T) {
			if got := CanTransition(tt.from, tt.to); got != tt.want {
				t.Fatalf("CanTransition(%q, %q) = %t, want %t", tt.from, tt.to, got, tt.want)
			}
		})
	}
}
