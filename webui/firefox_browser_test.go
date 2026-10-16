package webui

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func firefoxTestBinary(t *testing.T) string {
	t.Helper()
	candidates := []string{os.Getenv("NEXUS_FIREFOX"), "/Applications/Firefox.app/Contents/MacOS/firefox"}
	if p, e := exec.LookPath("firefox"); e == nil {
		candidates = append(candidates, p)
	}
	for _, p := range candidates {
		if p != "" && filepath.IsAbs(p) {
			if s, e := os.Stat(p); e == nil && !s.IsDir() {
				return p
			}
		}
	}
	if os.Getenv("NEXUS_REQUIRE_FIREFOX") == "1" {
		t.Fatal("Firefox is required but unavailable")
	}
	t.Skip("Firefox is not installed")
	return ""
}

func startFirefoxForTest(t *testing.T, ctx context.Context, binary string) (int, func()) {
	t.Helper()
	profile := t.TempDir()
	// These preferences affect only this owned test profile, not the user's browser.
	prefs := `user_pref("ui.prefersReducedMotion", 1);
user_pref("accessibility.tabfocus", 7);
user_pref("focusmanager.testmode", true);
user_pref("browser.shell.checkDefaultBrowser", false);
user_pref("browser.startup.homepage_override.mstone", "ignore");
user_pref("datareporting.policy.dataSubmissionEnabled", false);
`
	if e := os.WriteFile(filepath.Join(profile, "user.js"), []byte(prefs), 0600); e != nil {
		t.Fatal(e)
	}
	logPath := filepath.Join(profile, "remote.log")
	log, e := os.Create(logPath)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { log.Close() })
	args := []string{"--headless", "--no-remote", "--profile", profile, "--remote-debugging-port=0", "about:blank"}
	command := exec.CommandContext(ctx, binary, args...)
	// LaunchServices avoids macOS app-data protection denying a direct executable launch.
	if runtime.GOOS == "darwin" && strings.Contains(binary, ".app/Contents/MacOS/") {
		app := strings.Split(binary, ".app/Contents/MacOS/")[0] + ".app"
		launch := []string{"-n", "-a", app, "--stdout", logPath, "--stderr", logPath, "--env", "MOZ_ENABLE_FOCUS_TEST_MODE=1", "--args"}
		command = exec.CommandContext(ctx, "/usr/bin/open", append(launch, args...)...)
	}
	command.Env = append(os.Environ(), "MOZ_ENABLE_FOCUS_TEST_MODE=1")
	command.Stdout = log
	command.Stderr = log

	if e = command.Start(); e != nil {
		t.Fatal(e)
	}
	stopped := false
	ownedPID := command.Process.Pid
	launched := runtime.GOOS == "darwin" && strings.Contains(binary, ".app/Contents/MacOS/")
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	findOwner := func() {
		if !launched {
			return
		}
		out, _ := exec.Command("ps", "-axo", "pid=,args=").Output()
		for _, line := range strings.Split(string(out), "\n") {
			fields := strings.Fields(line)
			if len(fields) > 1 {
				args := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), fields[0]))
				if strings.HasPrefix(args, binary+" ") && strings.Contains(args, "--profile "+profile+" ") {
					if pid, err := strconv.Atoi(fields[0]); err == nil && pid > 1 {
						ownedPID = pid
						return
					}
				}
			}
		}
	}
	stop := func() {
		if stopped {
			return
		}
		stopped = true
		findOwner()
		if !launched || ownedPID != command.Process.Pid {
			_ = syscall.Kill(ownedPID, syscall.SIGINT)
			deadline := time.Now().Add(2 * time.Second)
			for time.Now().Before(deadline) && syscall.Kill(ownedPID, 0) == nil {
				time.Sleep(20 * time.Millisecond)
			}
			if syscall.Kill(ownedPID, 0) == nil {
				_ = syscall.Kill(ownedPID, syscall.SIGKILL)
			}
		}
		select {
		case <-done:
		case <-time.After(time.Second):
			_ = command.Process.Kill()
			<-done
		}
	}

	t.Cleanup(stop)
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		b, _ := os.ReadFile(filepath.Join(profile, "WebDriverBiDiServer.json"))
		var address struct {
			Port int `json:"ws_port"`
		}
		if json.Unmarshal(b, &address) == nil && address.Port > 0 {
			findOwner()
			return address.Port, stop
		}
		time.Sleep(20 * time.Millisecond)
	}
	stop()
	b, _ := os.ReadFile(logPath)
	t.Fatalf("Firefox remote agent unavailable: %s", b)
	return 0, func() {}
}

// Reuse the same API fixtures and assertions in the real Firefox engine.
// Unsupported automation commands fail; they never become skipped assertions.
func TestFirefoxWebUI(t *testing.T) {
	firefoxTestBinary(t)
	t.Setenv("NEXUS_TEST_BROWSER", "firefox")
	cases := []struct {
		name string
		run  func(*testing.T)
	}{
		{"AllPages", TestChromeSharedStyleAcrossPages},
		{"Uploads", TestChromeChatUploadsSelectionRecoveryAndSubmission},
		{"DownloadsComposer", TestChromeChatFilesPreviewDownloadAndReload},
		{"ComfyUI", TestChromeComfyWorkflowControls},
		{"Approvals", TestChromeRunningChatDiscoversLateApproval},
		{"ChatPreferences", TestChromeChatRenameAndPinSurviveRefresh},
		{"CloudConfiguration", TestChromeCloudModelConfigurationUsesCatalogAndExplicitSave},
		{"SettingsDraft", TestChromeLiveSettingsPreservesDirtyDraft},
		{"LiveStyles", TestChromeLiveStatsAndStylesWithoutReload},
		{"ChatRecovery", TestChromeChatRefreshFailureRetainsContent},
		{"WorkboardRecovery", TestChromeWorkboardRefreshRetainsVisibleSnapshot},
		{"SparkSidebar", TestChromeRemoteSidebarReloadAndLocalSwitchDoNotDispatch},
		{"SparkCSRF", TestChromeRemoteJobControlsAfterCSRFGrantEviction},
		{"Routing", TestChromeRoutingRemotePaths},
		{"Jobs", TestChromeJobStatusAndOpenCardUpdateInPlace},
		{"Inspector", TestChromeInspectorUpdatesValuesInPlace},
		{"Cron", TestChromeCronRetainsTransientFailureAndClearsRevocation},
		{"Collaboration", TestChromeModelCollaborationPage},
		{"Dependencies", TestChromeDependenciesStableMetadata},
		{"Logging", TestChromeLoggingStableMetadata},
		{"RemoteModels", TestChromeRemoteModelsAndStatusSeparation},
		{"RemoteOdometer", TestChromeRemoteOdometerBothLocalitiesAndUnavailable},
		{"Discovery", TestChromeRemoteDiscoveryPairingHandoff},
		{"Advertisement", TestChromeAdvertisementSettings},
	}
	for _, c := range cases {
		t.Run(c.name, c.run)
	}
}
