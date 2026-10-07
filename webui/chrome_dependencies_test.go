package webui

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestChromeDependenciesStableMetadata(t *testing.T) {
	chrome, node := chromeTestBinary(t), chromeTestNode(t)
	shell, err := NewShellHandler(ShellOptions{BasePath: "/app", HostAllowed: func(string) bool { return true }, Authenticated: func(*http.Request) bool { return true }})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/app/api/v1/dependencies" {
			writeChromeJSON(w, DependencyInventory{Version: 1, Hostname: "qa-host", ObservedAt: time.Now().UTC(), Items: []Dependency{{ID: "test", Name: "Example dependency", Status: "Not found", Scope: "Optional", Location: "/fixture/bin", Note: "Fixture metadata only"}}})
			return
		}
		shell.ServeHTTP(w, r)
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	port, stop := startChromeForTest(t, ctx, chrome)
	defer stop()
	script := strings.Split(chromeRemoteDiscoveryCDP, "await cdp('Page.navigate'")[0] + `
 await cdp('Page.navigate',{url:origin+'/app/dependencies'});
 await eventually('document.querySelectorAll(".logging-card").length===1','logging inventory failed');
 if(!await evaluate('document.querySelector("#dependencies-view").hidden===false&&document.querySelector("#chat-view").hidden&&document.querySelector(".logging-card").textContent.includes("Not found")&&document.querySelector(".logging-card").classList.contains("dependency-missing")&&document.querySelector(".dependency-host h2").textContent.includes("qa-host")&&document.querySelector("[data-view=dependencies]").getAttribute("aria-current")==="page"'))throw Error('logging page not selected or incomplete');
 await evaluate('window.cardBefore=document.querySelector(".logging-card");document.querySelector("#dependencies-refresh").click()');
 await eventually('!document.querySelector("#dependencies-refresh").disabled','refresh did not finish');
 if(!await evaluate('document.querySelector(".logging-card")===cardBefore'))throw Error('refresh replaced metadata card');
 await cdp('Emulation.setDeviceMetricsOverride',{width:390,height:844,deviceScaleFactor:1,mobile:false});
 if(!await evaluate('document.documentElement.scrollWidth<=innerWidth'))throw Error('logging page overflows narrow view');
 socket.close();`
	out, e := exec.CommandContext(ctx, node, "-e", script, strconv.Itoa(port), server.URL).CombinedOutput()
	if e != nil {
		t.Fatalf("%v %s", e, out)
	}
}
