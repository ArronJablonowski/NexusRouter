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
		Name string `yaml:"name"`
		On   map[string]struct {
			Inputs map[string]struct {
				Description string `yaml:"description"`
				Required    bool   `yaml:"required"`
				Type        string `yaml:"type"`
			} `yaml:"inputs"`
		} `yaml:"on"`
		Permissions map[string]string `yaml:"permissions"`
		Jobs        map[string]struct {
			Name     string            `yaml:"name"`
			Timeout  int               `yaml:"timeout-minutes"`
			Env      map[string]string `yaml:"env"`
			Strategy struct {
				FailFast bool `yaml:"fail-fast"`
				Matrix   struct {
					Include []struct {
						Target       string `yaml:"target"`
						Runner       string `yaml:"runner"`
						ExpectedOS   string `yaml:"expected_os"`
						ExpectedArch string `yaml:"expected_arch"`
					} `yaml:"include"`
				} `yaml:"matrix"`
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
	dispatch, ok := workflow.On["workflow_dispatch"]
	if !ok || len(dispatch.Inputs) != 2 {
		t.Fatal("dispatch authority changed")
	}
	versionInput, ok := dispatch.Inputs["version"]
	if !ok || !versionInput.Required || versionInput.Type != "string" || versionInput.Description == "" {
		t.Fatal("release version input changed")
	}
	evidenceInput, ok := dispatch.Inputs["license_evidence_sha256"]
	if !ok || !evidenceInput.Required || evidenceInput.Type != "string" || evidenceInput.Description == "" {
		t.Fatal("license evidence authority input changed")
	}
	if !reflect.DeepEqual(workflow.Permissions, map[string]string{"contents": "read"}) || len(workflow.Jobs) != 1 {
		t.Fatal("workflow authority expanded")
	}
	job, ok := workflow.Jobs["qualify"]
	expectedMatrix := [][4]string{
		{"darwin/amd64", "macos-15-intel", "darwin", "amd64"},
		{"darwin/arm64", "macos-15", "darwin", "arm64"},
		{"linux/amd64", "ubuntu-24.04", "linux", "amd64"},
		{"linux/arm64", "ubuntu-24.04-arm", "linux", "arm64"},
	}
	if !ok || job.Name != "Qualify ${{ matrix.target }}" || job.Timeout != 90 || job.Strategy.FailFast || job.RunsOn != "${{ matrix.runner }}" ||
		len(job.Strategy.Matrix.Include) != len(expectedMatrix) || len(job.Steps) != 8 {
		t.Fatal("unexpected job structure")
	}
	for i, expected := range expectedMatrix {
		got := job.Strategy.Matrix.Include[i]
		if [4]string{got.Target, got.Runner, got.ExpectedOS, got.ExpectedArch} != expected {
			t.Fatal("unexpected native runner matrix", i)
		}
	}
	if !reflect.DeepEqual(job.Env, map[string]string{
		"CGO_ENABLED": "0", "GODEBUG": "", "GOTOOLCHAIN": "local", "GOENV": "off",
		"GOEXPERIMENT": "", "GOFLAGS": "", "GOWORK": "off", "GOAMD64": "v1", "GOARM64": "v8.0",
	}) {
		t.Fatal("hosted qualification toolchain is not fail-closed")
	}
	if job.Steps[0].Uses != "actions/checkout@11d5960a326750d5838078e36cf38b85af677262" || job.Steps[0].With["ref"] != "${{ github.sha }}" || job.Steps[0].With["persist-credentials"] != false {
		t.Fatal("checkout not immutable or retains credentials")
	}
	if job.Steps[1].Uses != "actions/setup-go@40f1582b2485089dde7abd97c1529aa768e1baff" || job.Steps[1].With["go-version-file"] != "go.mod" || job.Steps[1].With["cache"] != false {
		t.Fatal("toolchain/cache contract")
	}
	evidence := job.Steps[3]
	if evidence.ID != "license_evidence" || evidence.Env["EXPECTED_LICENSE_EVIDENCE_SHA256"] != "${{ inputs.license_evidence_sha256 }}" ||
		!strings.Contains(evidence.Run, `license-evidence freeze --commit "$GITHUB_SHA"`) ||
		!strings.Contains(evidence.Run, `test "$actual" = "$EXPECTED_LICENSE_EVIDENCE_SHA256"`) ||
		!strings.Contains(evidence.Run, "make qualify-license-evidence") {
		t.Fatal("candidate license evidence is not independently bound and re-derived")
	}
	if job.Steps[4].ID != "check" || job.Steps[4].Run != "make check" || job.Steps[5].ID != "qualification" || job.Steps[5].Run != "make qualify-release" {
		t.Fatal("qualification gates bypassed")
	}
	if job.Steps[2].Env["RELEASE_VERSION"] != "${{ inputs.version }}" ||
		job.Steps[2].Env["EXPECTED_NATIVE_OS"] != "${{ matrix.expected_os }}" ||
		job.Steps[2].Env["EXPECTED_NATIVE_ARCH"] != "${{ matrix.expected_arch }}" ||
		!strings.Contains(job.Steps[2].Run, "version_pattern") || !strings.Contains(job.Steps[2].Run, "version=$RELEASE_VERSION") ||
		!strings.Contains(job.Steps[2].Run, `test "$(go env GOOS)" = "$EXPECTED_NATIVE_OS"`) ||
		!strings.Contains(job.Steps[2].Run, `test "$(go env GOARCH)" = "$EXPECTED_NATIVE_ARCH"`) ||
		!strings.Contains(job.Steps[2].Run, `test "$(go env GOHOSTOS)" = "$EXPECTED_NATIVE_OS"`) ||
		!strings.Contains(job.Steps[2].Run, `test "$(go env GOHOSTARCH)" = "$EXPECTED_NATIVE_ARCH"`) {
		t.Fatal("release version is not validated and recorded")
	}
	if !reflect.DeepEqual(job.Steps[5].Env, map[string]string{"DARWIN_RELEASE_VERSION": "${{ inputs.version }}", "DARWIN_RELEASE_COMMIT": "${{ github.sha }}"}) {
		t.Fatal("release identity is not bound to qualification")
	}
	for _, step := range job.Steps[2:] {
		if step.Uses != "" || step.Shell != "bash" {
			t.Fatal("additional external action or unexpected shell")
		}
	}
	for _, index := range []int{2, 6} {
		if !strings.Contains(job.Steps[index].Run, "git rev-parse HEAD") || !strings.Contains(job.Steps[index].Run, "$GITHUB_SHA") || !strings.Contains(job.Steps[index].Run, "git status --porcelain --untracked-files=all") {
			t.Fatal("missing source binding")
		}
	}
	report := job.Steps[7]
	if report.If != "${{ always() }}" || !strings.Contains(report.Run, "GITHUB_STEP_SUMMARY") || report.Env["QUALIFICATION_OUTCOME"] != "${{ steps.qualification.outcome }}" || report.Env["LICENSE_EVIDENCE_OUTCOME"] != "${{ steps.license_evidence.outcome }}" || report.Env["LICENSE_EVIDENCE_SHA256"] != "${{ inputs.license_evidence_sha256 }}" || report.Env["RELEASE_VERSION"] != "${{ steps.source.outputs.version }}" || !strings.Contains(report.Run, "Expected reviewed candidate license-evidence digest") {
		t.Fatal("missing failure-aware evidence")
	}
	for _, evidence := range []string{
		"four target-specific dependency closures and notice digests",
		"does not grant legal approval",
		"exact six-member schema-2 collateral",
		"target-specific dependency notices",
		"disposable native install",
		"schema-28-to-29 migration",
		"backup and rollback rehearsal",
		"installation outside the disposable runner-local rehearsal",
		"All four successful matrix jobs are required for four-target native evidence",
	} {
		if !strings.Contains(report.Run, evidence) {
			t.Fatal("hosted summary omits or misstates qualification evidence", evidence)
		}
	}
	for _, forbidden := range []string{"secrets.", "upload-artifact", "gh release", "git push", "git tag", "continue-on-error", "workflow_run", "pull_request"} {
		if strings.Contains(string(body), forbidden) {
			t.Fatal("unexpected publication or automatic authority", forbidden)
		}
	}
}
