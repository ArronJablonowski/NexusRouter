package webui

import (
	"strconv"
	"testing"
)

func TestRemoteDispatchBrowserReviewRecoveryAndNoReplay(t *testing.T) {
	script := string(mustAsset(t, "assets/v1/remote-dispatch.js"))
	runRoutingMapScript(t, `const vm=require('vm');function el(){return {children:[],handlers:{},hidden:false,disabled:false,value:'',textContent:'',setAttribute(){},append(...x){this.children.push(...x)},replaceChildren(){this.children=[]},addEventListener(k,f){this.handlers[k]=f}}};
 const parent=el(),document={createElement:el},records=[],window={location:{href:'http://localhost/app/settings'},history:{replaceState(a,b,url){window.location.href=String(url)}},NexusRemoteTaskControls:{attach(...args){records.push(args[2].request_id)}}};let calls=[];
 const fetch=async(url,opts)=>{const b=JSON.parse(opts.body);if(new URL(window.location.href).search!==''||new URLSearchParams(new URL(window.location.href).hash.slice(1)).get('remote_request')!==b.request_id)throw Error('sent before recovery link');calls.push(b);return {ok:false}};
 vm.runInNewContext(`+strconv.Quote(script)+`,{window,document,fetch,crypto:{randomUUID:()=> 'browser-request-000001'},URL,URLSearchParams});
 const peer={id:'node-a',models:['model-a'],harnesses:['pi']},tick=()=>new Promise(r=>setImmediate(r)),submit={preventDefault(){}};
 window.NexusRemoteDispatch.attach(parent,peer,'/app','csrf');const [title,notice,body,another]=parent.children[0].children,form=body.children[0],review=form.children[14],confirm=form.children[16],model=form.children[4].children[0],prompt=form.children[12].children[0];
 (async()=>{if(calls.length||!confirm.hidden||model.value)throw Error('implicit selection/dispatch');model.value='model-a';prompt.value='Instructions';form.handlers.submit(submit);if(confirm.hidden||calls.length)throw Error('review missing');form.handlers.input();confirm.handlers.click();await tick();if(calls.length)throw Error('stale review sent');form.handlers.submit(submit);confirm.handlers.click();confirm.handlers.click();await tick();if(calls.length!==1||calls[0].request_id!=='browser-request-000001'||records.length!==1||!notice.textContent.includes('could not be confirmed'))throw Error('uncertain delivery replayed');
 const reloaded=el();window.NexusRemoteDispatch.attach(reloaded,peer,'/app','csrf');if(calls.length!==1||records.length!==2||!reloaded.children[0].children[1].textContent.includes('Recovered'))throw Error('reload repeated inference');
 const broken=el();window.location.href='http://localhost/app/settings';window.history.replaceState=()=>{throw Error('URL update failed')};window.NexusRemoteDispatch.attach(broken,peer,'/app','csrf');const f=broken.children[0].children[2].children[0];f.children[4].children[0].value='model-a';f.children[12].children[0].value='Instructions';f.handlers.submit(submit);f.children[16].handlers.click();await tick();if(calls.length!==1||!broken.children[0].children[1].textContent.includes('No work was sent'))throw Error('recovery link failure dispatched');
 })().catch(e=>{console.error(e);process.exit(1)});`)
}
