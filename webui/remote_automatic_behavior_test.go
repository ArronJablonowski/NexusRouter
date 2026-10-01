package webui

import (
	"strconv"
	"testing"
)

func TestAutomaticRemoteBrowserReviewRecoveryAndNoReplay(t *testing.T) {
	script := string(mustAsset(t, "assets/v1/remote-automatic.js"))
	runRoutingMapScript(t, `const vm=require('vm');function el(){return {children:[],handlers:{},hidden:false,disabled:false,value:'',textContent:'',setAttribute(){},append(...x){this.children.push(...x)},replaceChildren(){this.children=[]},addEventListener(k,f){this.handlers[k]=f}}};
 const document={createElement:el},window={location:{href:'http://localhost/app/settings'},history:{replaceState(a,b,url){window.location.href=String(url)}},NexusRemoteTaskControls:{attach(parent,peer,task){attached.push({peer,task})}}};const attached=[],calls=[];let failStatus=false;
 const fetch=async(url,opts)=>{const b=JSON.parse(opts.body);if(new URL(window.location.href).search!==''||new URLSearchParams(new URL(window.location.href).hash.slice(1)).get('remote_auto_request')!==b.request_id)throw Error('request not saved');calls.push({url,body:b});if(url.endsWith('remote-auto-dispatch'))return {ok:false};return {ok:!failStatus,json:async()=>({version:1,request_id:b.request_id,instance:'node-selected',submission_id:'submission-a',state:'running'})}};
 vm.runInNewContext(`+strconv.Quote(script)+`,{document,window,fetch,crypto:{randomUUID:()=> 'automatic-request-001'},URL,URLSearchParams});
 const parent=el(),tick=()=>new Promise(r=>setImmediate(r)),event={preventDefault(){}};window.NexusRemoteAutomatic.attach(parent,'/app','csrf');
 const [title,notice,body,another]=parent.children[0].children,form=body.children[0],confirm=form.children[11];
 (async()=>{
 if(calls.length||!confirm.hidden)throw Error('implicit send');form.children[7].children[0].value='Instructions';form.handlers.submit(event);if(confirm.hidden||calls.length)throw Error('missing review');form.handlers.input();confirm.handlers.click();await tick();if(calls.length)throw Error('stale review sent');form.handlers.submit(event);confirm.handlers.click();confirm.handlers.click();await tick();
 if(calls.length!==1||calls[0].body.prompt!=='Instructions'||calls[0].body.private!==true||'destination' in calls[0].body||'allow_exploration' in calls[0].body||!notice.textContent.includes('could not be confirmed'))throw Error('unsafe dispatch');
 const reload=el();window.NexusRemoteAutomatic.attach(reload,'/app','csrf');if(calls.length!==1||!reload.children[0].children[1].textContent.includes('Recovered'))throw Error('reload replayed');
 const recovery=reload.children[0].children[2];recovery.children[1].handlers.click();recovery.children[1].handlers.click();await tick();if(calls.length!==2||Object.keys(calls[1].body).sort().join(',')!=='request_id,version'||attached.length!==1||attached[0].peer.id!=='node-selected')throw Error('status rerouted or retained prompt');
 failStatus=true;recovery.children[1].handlers.click();await tick();if(calls.length!==3||attached.length!==1||!reload.children[0].children[1].textContent.includes('not proof'))throw Error('unknown status relabeled');
 window.location.href='http://localhost/app/settings';window.history.replaceState=()=>{throw Error('failed')};const broken=el();window.NexusRemoteAutomatic.attach(broken,'/app','csrf');const f=broken.children[0].children[2].children[0];f.children[7].children[0].value='Instructions';f.handlers.submit(event);f.children[11].handlers.click();await tick();if(calls.length!==3||!broken.children[0].children[1].textContent.includes('No work was sent'))throw Error('unrecoverable dispatch');
 })().catch(e=>{console.error(e);process.exit(1)});`)
}
