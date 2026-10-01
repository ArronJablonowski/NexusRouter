package webuiapp

import (
	"encoding/json"
	"os"
	"os/exec"
	"testing"

	"github.com/ArronJablonowski/NexusRouter/remote"
)

func TestGuidedPairFormOutputMatchesRemotePeerContract(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node unavailable")
	}
	script, err := os.ReadFile("../../webui/assets/v1/remote-pair-form.js")
	if err != nil {
		t.Fatal(err)
	}
	source := `const window={};` + string(script) + `;const v={id:'node-a',endpoint:'https://127.0.0.1:8443',server_name:'node-a',pins:'a'.repeat(64),transport:'ssh',models:'model-a',harnesses:'pi,goose',max_cost:'1.25',max_context_tokens:'32768',op_info:true,op_inspect:true,op_dispatch:true,op_cancel:true,allow_private:true,allow_public_network:false,allow_cloud_inference:false,ssh_user:'router',ssh_port:'22',ssh_key:'/private/key',ssh_hosts:'/private/known_hosts',custom_limits:true,limit_info:'60',limit_inspect:'600',limit_dispatch:'60',limit_cancel:'120'};process.stdout.write(JSON.stringify([window.NexusRemotePairForm.build(v),window.NexusRemotePairForm.build({...v,transport:'https',custom_limits:false})]));`
	out, err := exec.Command(node, "-e", source).CombinedOutput()
	if err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	var peers []remote.Peer
	if err = json.Unmarshal(out, &peers); err != nil || len(peers) != 2 {
		t.Fatal(err, string(out))
	}
	for _, p := range peers {
		if err = p.Validate(); err != nil {
			t.Fatalf("%s: %v", p.Transport, err)
		}
	}
	if peers[0].SSH == nil || peers[0].RequestLimits == nil || peers[1].SSH != nil || peers[1].RequestLimits != nil {
		t.Fatal("transport-specific fields leaked")
	}
}
