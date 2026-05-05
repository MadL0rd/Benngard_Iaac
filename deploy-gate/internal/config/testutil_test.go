package config

import (
	"regexp"
	"testing"
)

func mustCompile(t *testing.T, pattern string) *regexp.Regexp {
	t.Helper()
	compiled, err := regexp.Compile(pattern)
	if err != nil {
		t.Fatalf("compile %q: %v", pattern, err)
	}
	return compiled
}
