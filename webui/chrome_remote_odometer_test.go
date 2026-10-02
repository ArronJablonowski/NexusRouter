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

func TestChromeRemoteOdometerBothLocalitiesAndUnavailable(t *testing.T) {
	chrome, node := chromeTestBinary(t), chromeTestNode(t)
	shell, e := NewShellHandler(ShellOptions{BasePath: "/app", HostAllowed: func(host string) bool { return strings.HasPrefix(host, "127.0.0.1:") }, Authenticated: func(r *http.Request) bool { return true }})
	if e != nil {
		t.Fatal(e)
	}
	count := func(in, out string, unknown int) map[string]any {
		return map[string]any{"input": in, "output": out, "measured": 1, "partial": unknown, "unknown": unknown}
	}
	meter := map[string]any{"lifetime": count("0", "0", 0), "trip": count("0", "0", 0), "revision": 0, "reset_at": ""}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/app/api/v1/stats" {
			writeChromeJSON(w, map[string]any{"version": 1, "cloud": meter, "local": meter, "unclassified": 0, "updated_at": time.Now().UTC(), "remote": map[string]any{"total": count("9007199254741093", "42", 1), "local": count("9007199254740993", "40", 0), "cloud": count("100", "2", 1), "requests": 2, "pending": 0, "unavailable": 1, "unclassified": 0, "last_sync": time.Now().UTC(), "sync_error": true}})
			return
		}
		shell.ServeHTTP(w, r)
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	port, stop := startChromeForTest(t, ctx, chrome)
	defer stop()
	preamble := strings.Split(chromeRemoteDiscoveryCDP, "await cdp('Page.navigate'")[0]
	steps := `
 await cdp('Page.navigate',{url:origin+'/app/stats'});
 await eventually('document.querySelector(".stats-remote")!==null','remote odometer missing');
 const text=await evaluate('document.querySelector(".stats-remote").textContent');
 for(const expected of ['Remote router odometer','Local models on remote routers','Cloud models on remote routers','9,007,199,254,741,093','actual usage may be higher','Reconciliation incomplete'])if(!text.includes(expected))throw Error(expected+': '+text);
 if(await evaluate('document.querySelectorAll(".stats-remote button").length'))throw Error('remote lifetime reset exposed');
 await cdp('Emulation.setDeviceMetricsOverride',{width:390,height:844,deviceScaleFactor:1,mobile:false});
 if(await evaluate('document.documentElement.scrollWidth>document.documentElement.clientWidth'))throw Error('mobile overflow');
 socket.close();`
	out, e := exec.CommandContext(ctx, node, "-e", preamble+steps, strconv.Itoa(port), server.URL).CombinedOutput()
	if e != nil {
		t.Fatalf("%v %s", e, out)
	}
}
