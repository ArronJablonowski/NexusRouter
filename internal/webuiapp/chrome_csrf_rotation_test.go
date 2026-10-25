package webuiapp

import (
	"context"
	"net/http/httptest"
	"os/exec"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/internal/browserauth"
	contract "github.com/ArronJablonowski/NexusRouter/webui"
)

func TestSettingsSaveAfterOtherControlsRotateCSRF(t *testing.T) {
	chrome := conflictChromeBinary(t)
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node unavailable")
	}
	auth, err := browserauth.New(browserauth.Options{SessionTTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	challenge, err := auth.Create()
	if err != nil {
		t.Fatal(err)
	}
	if err = auth.Approve(challenge.ID, challenge.DisplayCode); err != nil {
		t.Fatal(err)
	}
	session, err := auth.Consume(challenge.ID, challenge.Cookie)
	if err != nil {
		t.Fatal(err)
	}
	settings := contract.ToolAccessSettings{MacMemoryPercent: 100, MacSwapGrowthGB: 4}
	digest := strings.Repeat("a", 64)
	var calls atomic.Int32
	server := httptest.NewUnstartedServer(nil)
	handler, err := New(Options{BasePath: "/app", AllowedHosts: []string{server.Listener.Addr().String()}, Store: auth, Inspections: InspectionServices{Settings: func(context.Context) (contract.SettingsInspection, error) {
		return contract.SettingsInspection{Version: 1, Digest: digest, Active: settings, Saved: settings}, nil
	}}, Mutations: MutationServices{UpdateSettings: func(_ context.Context, input contract.SettingsUpdateRequest) (contract.SettingsInspection, error) {
		calls.Add(1)
		return contract.SettingsInspection{Version: 1, Digest: digest, Active: input.Settings, Saved: input.Settings}, nil
	}}})
	if err != nil {
		t.Fatal(err)
	}
	server.Config.Handler = handler
	server.Start()
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	port, stop := startConflictChrome(t, ctx, chrome)
	defer stop()
	script := strings.Split(chromeConflictCDP, "await cdp('Page.navigate'")[0] + `
await cdp('Page.navigate',{url:origin+'/app/settings'});
await eventually('!document.querySelector("#save-settings")?.disabled','Settings did not load');
await evaluate('(async()=>{for(let i=0;i<10;i++){const r=await fetch("/app/api/v1/session/csrf",{method:"POST",credentials:"same-origin",headers:{"Content-Type":"application/json"},body:JSON.stringify({version:1})});if(!r.ok)throw Error("rotation failed");}})()');
await evaluate('document.querySelector("#auto-approve-tools").checked=true;document.querySelector("#save-settings").click()');
await eventually('document.querySelector("#settings-status").textContent.includes("match the running daemon")','Settings save rejected after other controls rotated the session grant');
socket.close();`
	output, err := exec.CommandContext(ctx, node, "-e", script, strconv.Itoa(port), server.URL, session.Token).CombinedOutput()
	if err != nil {
		t.Fatalf("%v %s", err, output)
	}
	if calls.Load() != 1 {
		t.Fatal("settings update missing or replayed", calls.Load())
	}
}
