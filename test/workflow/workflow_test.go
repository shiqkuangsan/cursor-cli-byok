package workflow_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

type workflow struct {
	Jobs map[string]workflowJob `yaml:"jobs"`
}

type workflowJob struct {
	Steps []workflowStep `yaml:"steps"`
}

type workflowStep struct {
	Name string `yaml:"name"`
	Run  string `yaml:"run"`
	Uses string `yaml:"uses"`
}

func TestReleaseValidatesTaggedArtifactsBeforePublishing(t *testing.T) {
	workflow := loadWorkflow(t, "release.yml")
	steps := workflow.Jobs["release"].Steps
	build := requireStep(t, steps, "Build release assets")
	amd64Smoke := requireStep(t, steps, "Run tagged Linux amd64 lifecycle smoke")
	arm64Smoke := requireStep(t, steps, "Run tagged Linux arm64 lifecycle smoke")
	e2e := requireStep(t, steps, "Run tagged Linux amd64 E2E")
	checksums := requireStep(t, steps, "Verify release checksums")
	publish := requireStep(t, steps, "Publish GitHub release")

	for name, index := range map[string]int{
		"amd64 lifecycle": amd64Smoke,
		"arm64 lifecycle": arm64Smoke,
		"amd64 E2E":       e2e,
		"checksums":       checksums,
	} {
		if index <= build || index >= publish {
			t.Fatalf("%s step index = %d, want after build %d and before publish %d", name, index, build, publish)
		}
	}
	if run := steps[e2e].Run; !strings.Contains(run, "E2E_BYOK_BINARY") || !strings.Contains(run, "cursor-cli-byok-linux-amd64") || !strings.Contains(run, "E2E_HELPER_BINARY") {
		t.Fatalf("tagged E2E run does not select the release artifact and prebuilt helper: %q", run)
	}
	if run := steps[arm64Smoke].Run; !strings.Contains(run, "--platform linux/arm64") || !strings.Contains(run, "cursor-cli-byok-linux-arm64") {
		t.Fatalf("arm64 lifecycle run does not execute the tagged arm64 artifact: %q", run)
	}
	if !strings.Contains(steps[checksums].Run, "sha256sum -c checksums.txt") {
		t.Fatalf("checksum step does not verify the release manifest: %q", steps[checksums].Run)
	}
}

func TestOfficialE2EGatesUseLatestVerifiedCursorVersion(t *testing.T) {
	cases := []struct {
		workflow string
		job      string
		step     string
	}{
		{workflow: "ci.yml", job: "linux-e2e", step: "Run verified Linux E2E"},
		{workflow: "release.yml", job: "release", step: "Run tagged Linux amd64 E2E"},
	}
	for _, testCase := range cases {
		t.Run(testCase.workflow, func(t *testing.T) {
			workflow := loadWorkflow(t, testCase.workflow)
			steps := workflow.Jobs[testCase.job].Steps
			index := requireStep(t, steps, testCase.step)
			run := steps[index].Run
			if !strings.Contains(run, "internal/cursorcli/verified_versions.txt") || !strings.Contains(run, "E2E_EXPECT_CURSOR_VERSION") {
				t.Fatalf("official E2E gate is not bound to the verified-version manifest: %q", run)
			}
		})
	}
}

func loadWorkflow(t *testing.T, name string) workflow {
	t.Helper()
	path := filepath.Join("..", "..", ".github", "workflows", name)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", path, err)
	}
	var result workflow
	if err := yaml.Unmarshal(data, &result); err != nil {
		t.Fatalf("Unmarshal(%q) error = %v", path, err)
	}
	return result
}

func requireStep(t *testing.T, steps []workflowStep, name string) int {
	t.Helper()
	for index, step := range steps {
		if step.Name == name {
			return index
		}
	}
	t.Fatalf("workflow step %q is missing", name)
	return -1
}
