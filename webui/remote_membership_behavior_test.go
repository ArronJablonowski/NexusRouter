package webui

import (
	"strconv"
	"testing"
)

func TestRemoteMembershipBrowserFencesAndUncertainWrites(t *testing.T) {
	script := string(mustAsset(t, "assets/v1/remote-membership.js"))
	runRoutingMapScript(t, `const vm=require('vm');
 const nodes=new Map();
 function element(){return {focus(){},setAttribute(){},textContent:'',value:'',checked:false,hidden:false,disabled:false,children:[],handlers:{},addEventListener(k,f){this.handlers[k]=f},append(...c){this.children.push(...c)},replaceChildren(){this.children=[]},querySelectorAll(){return this.children.flatMap(c=>c.children).filter(c=>c.type==='button')}}}
 const document={querySelector(k){if(!nodes.has(k))nodes.set(k,element());return nodes.get(k)},createElement:element};
 const peer={id:'<script>no</script>',endpoint:'https://127.0.0.1:8443',transport:'ssh'};
 const page={version:1,enabled:true,inspection_enabled:true,digest:'a'.repeat(64),registry:{version:1,peers:[peer]}};
 let posts=[],failure=false; const fetch=async(url,opts)=>{if(opts.method==='POST'){posts.push(JSON.parse(opts.body));if(failure)return {ok:false,status:409}}return {ok:true,json:async()=>page}};
 let clears=0; const window={NexusRemoteInspection:{attach(card){const button=element();button.type="button";button.disabled=true;card.append(button);}},NexusRemotePairForm:{mount(){return {read:()=>peer,lock(){},clear(){clears++;}}}}};vm.runInNewContext(`+strconv.Quote(script)+`,{window,document,fetch});
 const tick=()=>new Promise(resolve=>setImmediate(resolve));
 (async()=>{
 window.NexusRemoteMembership.mount('/console','token');await tick();
 const form=nodes.get('#remote-pair-form'),verified=nodes.get('#remote-identity-verified'),peers=nodes.get('#remote-peers');
 if(form.hidden||peers.children.length!==1||peers.children[0].children[0].textContent!==peer.id+' · ssh · '+peer.endpoint)throw Error('membership render');
 if(!peers.children[0].children[4].disabled)throw Error('membership refresh enabled independent task control');
 form.handlers.submit({preventDefault(){}});await tick();if(posts.length)throw Error('unverified pairing');
 verified.checked=true;form.handlers.submit({preventDefault(){}});await tick();if(posts.length!==1||!posts[0].identity_verified||posts[0].expected_digest!==page.digest||clears!==1||verified.checked)throw Error('pair receipt');
 failure=true;let card=peers.children[0],confirmation=card.children[3];card.children[2].handlers.click();await tick();if(posts.length!==1||confirmation.hidden)throw Error('revoked without confirmation');confirmation.children[2].handlers.click();confirmation.children[1].handlers.click();await tick();if(posts.length!==1)throw Error('dismiss revoked');
 card.children[2].handlers.click();nodes.get('#remote-refresh').handlers.click();await tick();confirmation.children[1].handlers.click();await tick();if(posts.length!==1)throw Error('stale revoke');card=peers.children[0];card.children[2].handlers.click();card.children[3].children[1].handlers.click();card.children[3].children[1].handlers.click();await tick();
 if(posts.length!==2||posts[1].action!=='revoke'||posts[1].instance!==peer.id||!form.hidden||peers.children.length)throw Error('conflict retained actionable stale state');
 verified.checked=true;form.handlers.submit({preventDefault(){}});await tick();if(posts.length!==2)throw Error('replayed uncertain mutation');
 if(!nodes.get('#remote-status').textContent.includes('Refresh'))throw Error('missing recovery instruction');
 nodes.get('#remote-refresh').handlers.click();await tick();if(form.hidden||peers.children.length!==1)throw Error('refresh failed');
 })().catch(e=>{console.error(e);process.exit(1)});`)
}
