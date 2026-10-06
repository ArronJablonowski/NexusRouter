package remote

import (
	"context"
	"os"
	"testing"
)

func TestControllerAuthorityAndMetadata(t *testing.T) {
	peer := Peer{ID: "studio", Endpoint: "https://10.0.0.2:9443", Operations: []string{"info"}}
	if requestingController(peer, "studio.local") != nil {
		t.Fatal("inspection peer labeled controller")
	}
	peer.Operations = append(peer.Operations, "dispatch")
	c := requestingController(peer, "studio.local")
	if c == nil || c.Hostname != "studio.local" || c.IP != "10.0.0.2" || !c.valid() {
		t.Fatal(c)
	}
	if requestingController(peer, "bad\nhost").Hostname != "" {
		t.Fatal("unsafe hostname")
	}
	if (&Controller{Instance: "studio", IP: "not-an-ip"}).valid() {
		t.Fatal("bad IP accepted")
	}
}
func TestAuthenticatedControllerReported(t *testing.T) {
	f := setup(t)
	info, err := f.client.Info(context.Background(), "node-a")
	if err != nil {
		t.Fatal(err)
	}
	hostname, _ := os.Hostname()
	if info.Controller == nil || info.Controller.Hostname != hostname || info.Controller.IP == "" {
		t.Fatal("controller metadata missing", info.Controller)
	}
}
