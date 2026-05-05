package config

import "testing"

// TestImagePatterns — contract for the regexes Ansible renders into
// config.json. Locks registry/namespace/repo (security: can't deploy
// arbitrary image) and shell-safe tag charset (security: can't inject
// into ssh command). Tag *format* discipline is the GH workflow's job.
func TestImagePatterns(t *testing.T) {
	backendRegex := mustCompile(t,
		`^exo\.container-registry\.com/benngard-family-office-ltd/benngard-backend:[a-zA-Z0-9._-]+$`)
	frontendRegex := mustCompile(t,
		`^exo\.container-registry\.com/benngard-family-office-ltd/benngard-frontend:[a-zA-Z0-9._-]+$`)

	cases := []struct {
		name       string
		whichRegex string // "backend" or "frontend"
		image      string
		want       bool
	}{
		// Happy path — semver of any shape passes.
		{"backend semver", "backend", "exo.container-registry.com/benngard-family-office-ltd/benngard-backend:v1.2.3", true},
		{"backend dev suffix", "backend", "exo.container-registry.com/benngard-family-office-ltd/benngard-backend:v1.2.3-dev.4", true},
		{"backend rc suffix", "backend", "exo.container-registry.com/benngard-family-office-ltd/benngard-backend:v1.2.3-rc.1", true},
		{"backend calver", "backend", "exo.container-registry.com/benngard-family-office-ltd/benngard-backend:v2026.05.21.1", true},
		{"frontend semver", "frontend", "exo.container-registry.com/benngard-family-office-ltd/benngard-frontend:v1.2.3", true},
		{"frontend bare -dev", "frontend", "exo.container-registry.com/benngard-family-office-ltd/benngard-frontend:v1.2.3-dev", true},

		// Wrong-image rejections — registry/namespace/repo are security-critical.
		{"wrong registry", "backend", "evil.com/benngard-family-office-ltd/benngard-backend:v1.2.3", false},
		{"wrong namespace", "backend", "exo.container-registry.com/other-org/benngard-backend:v1.2.3", false},
		{"wrong repo", "backend", "exo.container-registry.com/benngard-family-office-ltd/other:v1.2.3", false},
		{"cross-service (backend regex, frontend image)", "backend", "exo.container-registry.com/benngard-family-office-ltd/benngard-frontend:v1.2.3", false},

		// Shell-injection rejections — these would break out of ssh command.
		{"injection: semicolon", "backend", "exo.container-registry.com/benngard-family-office-ltd/benngard-backend:v1.2.3;rm", false},
		{"injection: backtick", "backend", "exo.container-registry.com/benngard-family-office-ltd/benngard-backend:v1.2.3`whoami`", false},
		{"injection: dollar-paren", "backend", "exo.container-registry.com/benngard-family-office-ltd/benngard-backend:v1.2.3$(id)", false},
		{"injection: space", "backend", "exo.container-registry.com/benngard-family-office-ltd/benngard-backend:v1.2.3 rm", false},
		{"injection: pipe", "backend", "exo.container-registry.com/benngard-family-office-ltd/benngard-backend:v1.2.3|cat", false},
		{"injection: newline", "backend", "exo.container-registry.com/benngard-family-office-ltd/benngard-backend:v1.2.3\nrm", false},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			regex := backendRegex
			if testCase.whichRegex == "frontend" {
				regex = frontendRegex
			}
			result := regex.MatchString(testCase.image)
			if result != testCase.want {
				t.Errorf("match(%q) = %v, want %v", testCase.image, result, testCase.want)
			}
		})
	}
}

func TestIsApprover(t *testing.T) {
	secrets := &Secrets{ApproverIDs: []int64{111, 222}}
	if !secrets.IsApprover(111) {
		t.Error("expected 111 to be approver")
	}
	if secrets.IsApprover(333) {
		t.Error("did not expect 333 to be approver")
	}
}
