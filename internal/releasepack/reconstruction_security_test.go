package releasepack

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSecurityReconstructionEnvironmentIsAnAllowlist(t *testing.T) {
	workspace, err := newGoReconstruction([]string{
		"PATH=" + filepath.Dir(mustSecurityGoExecutable(t)),
		"HOME=/ambient/home",
		"HTTP_PROXY=https://user:secret@proxy.invalid",
		"SSL_CERT_FILE=/ambient/private-ca.pem",
		"NETRC=/ambient/netrc",
		"GH_TOKEN=ambient-secret",
		"GOPROXY=direct",
		"GOSUMDB=off",
	}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if closeErr := workspace.close(); closeErr != nil {
			t.Fatal(closeErr)
		}
	}()
	if !validReconstructionEnvironment(workspace.env, workspace) {
		t.Fatal("constructed reconstruction environment is not exact")
	}
	joined := strings.Join(workspace.env, "\n")
	for _, forbidden := range []string{
		"ambient", "user:secret", "HTTP_PROXY=", "HTTPS_PROXY=", "ALL_PROXY=", "NO_PROXY=",
		"SSL_CERT_FILE=", "NETRC=", "GH_TOKEN=", "GOPROXY=direct", "GOSUMDB=off",
	} {
		if strings.Contains(joined, forbidden) {
			t.Fatalf("ambient environment escaped reconstruction allowlist: %q", forbidden)
		}
	}
}

func TestSecurityApprovalCallGraphProtectsReleaseAndInstallRoots(t *testing.T) {
	verification, err := os.ReadFile("approved_verification.go")
	if err != nil {
		t.Fatal(err)
	}
	verificationText := string(verification)
	if !strings.Contains(verificationText, "protectedPaths = append([]string{options.Dir}, protectedPaths...)") ||
		!strings.Contains(verificationText, "options.ExpectedLicenseEvidenceSHA256, options.Source, protectedPaths...)") {
		t.Fatal("approval-bound license reconstruction no longer protects the release root")
	}
	install, err := os.ReadFile("approved_install.go")
	if err != nil {
		t.Fatal(err)
	}
	if count := strings.Count(string(install), "verifyApproved(ctx, options.Verification, options.InstallRoot)"); count != 2 {
		t.Fatalf("install root is not protected across both approval checks: count=%d", count)
	}
}

func mustSecurityGoExecutable(t *testing.T) string {
	t.Helper()
	identity, err := pinReleaseExecutable(environment(), "go")
	if err != nil {
		t.Fatal(err)
	}
	return identity.path
}
