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

func TestChromeLoggingStableMetadata(t *testing.T) {
	chrome, node := chromeTestBinary(t), chromeTestNode(t)
	shell, err := NewShellHandler(ShellOptions{BasePath: "/app", HostAllowed: func(string) bool { return true }, Authenticated: func(*http.Request) bool { return true }})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/app/api/v1/logging" {
			size, entries := int64(2048), int64(12)
			writeChromeJSON(w, LoggingPage{TotalBytes: size, TotalEntries: entries, Version: 1, ObservedAt: time.Now().UTC(), Items: []LogLocation{{Bytes: &size, Entries: &entries, Measurement: "Database rows", ID: "runtime", Name: "Task history", Location: "/private/fixture/runtime.db", Scope: "Local", Format: "SQLite", Status: "Present", Records: []string{"Input/output tokens", "Approval actor and timestamp"}, Note: "Fixture metadata only"}}})
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
 await cdp('Page.navigate',{url:origin+'/app/logging'});
 await eventually('document.querySelectorAll(".logging-card").length===1','logging inventory failed');
 if(!await evaluate('document.querySelector("#logging-view").hidden===false&&document.querySelector("#chat-view").hidden&&document.querySelector(".logging-card").textContent.includes("Approval actor")&&document.querySelector("[data-view=logging]").getAttribute("aria-current")==="page"'))throw Error('logging page not selected or incomplete');
 if(!await evaluate('document.querySelector("#logging-totals").textContent.includes("2 KiB")&&document.querySelector(".logging-storage").textContent.includes("12")'))throw Error('missing sizes/counts');
 await evaluate('window.cardBefore=document.querySelector(".logging-card");document.querySelector("#logging-refresh").click()');
 await eventually('!document.querySelector("#logging-refresh").disabled','refresh did not finish');
 if(!await evaluate('document.querySelector(".logging-card")===cardBefore'))throw Error('refresh replaced metadata card');
 await cdp('Emulation.setDeviceMetricsOverride',{width:390,height:844,deviceScaleFactor:1,mobile:false});
 if(!await evaluate('document.documentElement.scrollWidth<=innerWidth'))throw Error('logging page overflows narrow view');
 socket.close();`
	out, e := exec.CommandContext(ctx, node, "-e", script, strconv.Itoa(port), server.URL).CombinedOutput()
	if e != nil {
		t.Fatalf("%v %s", e, out)
	}
}
