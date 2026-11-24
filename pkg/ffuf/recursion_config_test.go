package ffuf

import (
	"context"
	"strings"
	"testing"
)

func TestRecursionConfigValidation(t *testing.T) {
	tests := []struct {
		name     string
		depth    int
		strategy string
		wantErr  string
	}{
		{name: "negative depth", depth: -1, strategy: "greedy", wantErr: "-recursion-depth"},
		{name: "unknown strategy", depth: 2, strategy: "sideways", wantErr: "-recursion-strategy"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts := NewConfigOptions()
			opts.HTTP.URL = "https://example.org/FUZZ"
			opts.Input.Wordlists = []string{"-"}
			opts.HTTP.Recursion = true
			opts.HTTP.RecursionDepth = tt.depth
			opts.HTTP.RecursionStrategy = tt.strategy

			_, err := ConfigFromOptions(opts, context.Background(), func() {})
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("ConfigFromOptions error = %v, want one mentioning %q", err, tt.wantErr)
			}
		})
	}
}
