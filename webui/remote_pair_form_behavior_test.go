package webui

import (
	"strconv"
	"testing"
)

func TestGuidedRemotePairingPreservesExplicitScopes(t *testing.T) {
	script := string(mustAsset(t, "assets/v1/remote-pair-form.js"))
	runRoutingMapScript(t, `const vm=require('vm'),window={};vm.runInNewContext(`+strconv.Quote(script)+`,{window});const build=window.NexusRemotePairForm.build;
 const v={id:'node-a',endpoint:'https://192.168.1.20:8443',server_name:'node-a',pins:Array(32).fill('AA').join(':'),transport:'https',models:'model-a\nmodel-b',harnesses:'pi,goose',max_cost:'0',max_context_tokens:'32768',op_info:true,op_inspect:false,op_dispatch:false,op_cancel:false,allow_private:false,allow_public_network:false,allow_cloud_inference:false,ssh_user:'router',ssh_port:'22',ssh_key:'/private/key',ssh_hosts:'/private/known_hosts',custom_limits:false};
 let p=build(v);if(p.pins[0]!=='a'.repeat(64)||p.operations.join(',')!=='info'||p.models.join(',')!=='model-a,model-b'||p.harnesses.join(',')!=='pi,goose'||p.ssh||p.request_limits||p.allow_private||p.allow_public_network||p.allow_cloud_inference)throw Error('implicit authority or wrong normalization');
 if(build({...v,ssh_port:'bad',limit_info:'0'}).ssh)throw Error('inactive fields used');
 p=build({...v,transport:'ssh',op_cancel:true,allow_private:true,custom_limits:true,limit_info:'60',limit_inspect:'600',limit_dispatch:'60',limit_cancel:'120'});if(p.ssh.port!==22||p.ssh.known_hosts_file!=='/private/known_hosts'||p.request_limits.cancel!==120||!p.operations.includes('cancel')||!p.allow_private)throw Error('SSH scopes lost');
 for(const bad of [{pins:'bad'},{pins:'a'.repeat(64)+'\n'+'a'.repeat(64)},{endpoint:'https://example.com:443'},{endpoint:'https://127.0.0.1:0'},{models:'auto'},{models:'model-a,model-a'},{transport:'ssh',ssh_hosts:''},{transport:'ssh',ssh_port:'65536'},{max_cost:'Infinity'},{max_context_tokens:'1.5'},{op_info:false},{custom_limits:true,limit_info:'0'}]){let failed=false;try{build({...v,...bad})}catch{failed=true}if(!failed)throw Error('invalid pairing accepted '+JSON.stringify(bad));}`)
}
func TestGuidedRemotePairingClearsVerificationWhenFieldsChange(t *testing.T) {
	script := string(mustAsset(t, "assets/v1/remote-pair-form.js"))
	runRoutingMapScript(t, `const vm=require('vm'),nodes=new Map(),window={};
 const document={querySelector(id){if(!nodes.has(id))nodes.set(id,{value:'',checked:false,hidden:false,disabled:false,textContent:''});return nodes.get(id)}};
 const fields={'peer-id':'node-a','peer-endpoint':'https://127.0.0.1:8443','peer-server-name':'node-a','peer-pins':'a'.repeat(64),'peer-transport':'https','peer-context':'32768','peer-cost':'0'};
 for(const [key,value]of Object.entries(fields))document.querySelector('#remote-'+key).value=value;document.querySelector('#remote-op-info').checked=true;
 const form={handlers:{},addEventListener(k,f){this.handlers[k]=f},querySelectorAll(){return [...nodes.values()]},reset(){for(const n of nodes.values()){n.value="";n.checked=false;}for(const [key,value]of Object.entries(fields))document.querySelector("#remote-"+key).value=value;document.querySelector("#remote-op-info").checked=true;}},verified={checked:true};
 vm.runInNewContext(`+strconv.Quote(script)+`,{window,document});const editor=window.NexusRemotePairForm.mount(form,verified);
 if(verified.checked||!nodes.get('#remote-peer-preview').textContent.includes('node-a')||!nodes.get('#remote-ssh-fields').hidden)throw Error('initial review invalid');
 verified.checked=true;form.handlers.change({target:verified});if(!verified.checked)throw Error('verification cannot be selected');
 const cloud=nodes.get('#remote-peer-cloud');cloud.checked=true;form.handlers.input({target:cloud});if(verified.checked||!editor.read().allow_cloud_inference)throw Error('scope edit kept old verification');
 verified.checked=true;editor.prefill({instance:"discovered",endpoint:"https://192.168.1.20:8443",server_name:"new.local",claimed_certificate_sha256:"b".repeat(64),ssh_port:2222});if(verified.checked||editor.read().id!=="discovered"||editor.read().allow_cloud_inference||editor.read().transport!=="https"||nodes.get("#remote-peer-ssh-port").value!=="2222")throw Error("prefill retained authority");
 verified.checked=true;editor.clear();if(verified.checked)throw Error('reset retained consent');editor.lock(true);if([...nodes.values()].some(n=>!n.disabled))throw Error('inflight edits enabled');editor.lock(false);
 `)
}
