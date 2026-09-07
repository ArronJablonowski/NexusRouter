package releasepack

import (
	"os"
	"reflect"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

// Static guardrails only: this does not execute Actions or validate hosted
// runner/toolchain availability and cannot establish hosted qualification.
func TestReleaseQualificationWorkflowAuthority(t *testing.T) {
	body, err := os.ReadFile("../../.github/workflows/release-qualification.yml")
	if err != nil {
		t.Fatal(err)
	}
	var workflow struct {
		Name        string            `yaml:"name"`
		On          map[string]any    `yaml:"on"`
		Permissions map[string]string `yaml:"permissions"`
		Jobs        map[string]struct {
			Name     string `yaml:"name"`
			Timeout  int    `yaml:"timeout-minutes"`
			Strategy struct {
				FailFast bool                `yaml:"fail-fast"`
				Matrix   map[string][]string `yaml:"matrix"`
			} `yaml:"strategy"`
			RunsOn string `yaml:"runs-on"`
			Steps  []struct {
				Name, ID, Uses, Shell, Run, If string
				With                           map[string]any
				Env                            map[string]string
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	d := yaml.NewDecoder(strings.NewReader(string(body)))
	d.KnownFields(true)
	if err := d.Decode(&workflow); err != nil {
		t.Fatal("invalid workflow YAML", err)
	}
	if len(workflow.On) != 1 {
		t.Fatal("automatic trigger introduced")
	}
	if value, ok := workflow.On["workflow_dispatch"]; !ok || value != nil {
		t.Fatal("dispatch authority changed")
	}
	if !reflect.DeepEqual(workflow.Permissions, map[string]string{"contents": "read"}) || len(workflow.Jobs) != 1 {
		t.Fatal("workflow authority expanded")
	}
	job, ok := workflow.Jobs["qualify"]
	if !ok || job.Timeout != 90 || job.Strategy.FailFast || job.RunsOn != "${{ matrix.os }}" || !reflect.DeepEqual(job.Strategy.Matrix["os"], []string{"ubuntu-latest", "macos-latest"}) || len(job.Steps) != 7 {
		t.Fatal("unexpected job structure")
	}
	if job.Steps[0].Uses != "actions/checkout@v4" || job.Steps[0].With["ref"] != "${{ github.sha }}" || job.Steps[0].With["persist-credentials"] != false {
		t.Fatal("checkout not immutable or retains credentials")
	}
	if job.Steps[1].Uses != "actions/setup-go@v5" || job.Steps[1].With["go-version-file"] != "go.mod" || job.Steps[1].With["cache"] != false {
		t.Fatal("toolchain/cache contract")
	}
	if job.Steps[3].ID != "check" || job.Steps[3].Run != "make check" || job.Steps[4].ID != "qualification" || job.Steps[4].Run != "make qualify-release" {
		t.Fatal("qualification gates bypassed")
	}
	for _, step := range job.Steps[2:] {
		if step.Uses != "" || step.Shell != "bash" {
			t.Fatal("additional external action or unexpected shell")
		}
	}
	for _, index := range []int{2, 5} {
		if !strings.Contains(job.Steps[index].Run, "git rev-parse HEAD") || !strings.Contains(job.Steps[index].Run, "$GITHUB_SHA") || !strings.Contains(job.Steps[index].Run, "git status --porcelain --untracked-files=all") {
			t.Fatal("missing source binding")
		}
	}
	report := job.Steps[6]
	if report.If != "${{ always() }}" || !strings.Contains(report.Run, "GITHUB_STEP_SUMMARY") || report.Env["QUALIFICATION_OUTCOME"] != "${{ steps.qualification.outcome }}" {
		t.Fatal("missing failure-aware evidence")
	}
	for _, forbidden := range []string{"secrets.", "upload-artifact", "gh release", "git push", "git tag", "continue-on-error", "workflow_run", "pull_request"} {
		if strings.Contains(string(body), forbidden) {
			t.Fatal("unexpected publication or automatic authority", forbidden)
		}
	}
}
