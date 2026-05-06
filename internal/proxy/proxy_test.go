package proxy

import "testing"

func TestMatchHost(t *testing.T) {
	tests := []struct {
		host, pattern string
		want          bool
	}{
		{"q.us-east-1.amazonaws.com", "q.*.amazonaws.com", true},
		{"q.eu-west-1.amazonaws.com", "q.*.amazonaws.com", true},
		{"q.us-east-1.amazonaws.com", "q.us-east-1.amazonaws.com", true},
		{"other.amazonaws.com", "q.*.amazonaws.com", false},
		{"q.us-east-1.other.com", "q.*.amazonaws.com", false},
		{"Q.US-EAST-1.AMAZONAWS.COM", "q.*.amazonaws.com", true},
	}
	for _, tt := range tests {
		got := matchHost(tt.host, tt.pattern)
		if got != tt.want {
			t.Errorf("matchHost(%q, %q) = %v, want %v", tt.host, tt.pattern, got, tt.want)
		}
	}
}
