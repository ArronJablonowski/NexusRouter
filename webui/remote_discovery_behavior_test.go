package webui

import (
	"strconv"
	"testing"
)

func TestRemoteDiscoveryBrowserExplicitScanAndExpiry(t *testing.T) {
	script := string(mustAsset(t, "assets/v1/remote-discovery.js"))
	runRoutingMapScript(t, `const vm=require('vm');const nodes=new Map();function element(){return {hidden:false,disabled:false,textContent:'',children:[],handlers:{},addEventListener(k,f){this.handlers[k]=f},append(...c){this.children.push(...c)},replaceChildren(){this.children=[]}}}const document={querySelector(k){if(!nodes.has(k))nodes.set(k,element());return nodes.get(k)},createElement:element};
 let posts=[],selected=[],release;const now=Date.now();let clock=now;
 let candidate={version:1,verified:false,instance:'node-a',endpoint:'https://192.168.1.20:8443',server_name:'node-a.local',claimed_certificate_sha256:'a'.repeat(64),ssh_port:22,expires_at:new Date(now+30000).toISOString()};
 const fetch=async(url,opts)=>{posts.push({url,opts});await new Promise(r=>release=r);return {ok:true,json:async()=>({version:1,candidates:[candidate]})}};
 const window={};vm.runInNewContext(`+strconv.Quote(script)+`,{window,document,fetch,AbortController,setTimeout,clearTimeout,Date:{now:()=>clock,parse:Date.parse}});
 const tick=()=>new Promise(r=>setImmediate(r));
 (async()=>{
 const view=window.NexusRemoteDiscovery.mount('/console','csrf',c=>selected.push(c));const scan=nodes.get('#remote-discover'),results=nodes.get('#remote-discovery-results');
 scan.handlers.click();await tick();if(posts.length)throw Error('disabled scan');view.setEnabled(true);if(posts.length)throw Error('automatic scan');
 const first=scan.handlers.click();scan.handlers.click();await tick();if(posts.length!==1)throw Error('duplicate scan');const p=posts[0];if(p.url!=='/console/api/v1/remote-discovery'||p.opts.headers['X-Darwin-CSRF']!=='csrf'||p.opts.body!=='{"version":1}')throw Error('scan authority');release();await first;
 if(results.children.length!==1||!results.children[0].children[0].textContent.includes('Unverified')||selected.length)throw Error('unsafe result');const choose=results.children[0].children[2];choose.handlers.click();if(selected.length!==1||selected[0].verified)throw Error('selection');clock=now+31000;choose.handlers.click();if(selected.length!==1||!choose.disabled)throw Error('expired selection');
 clock=now;const next=scan.handlers.click();await tick();view.setEnabled(false);release();await next;if(results.children.length||!nodes.get('#remote-discovery').hidden)throw Error('late disabled response');
 view.setEnabled(true);candidate={...candidate,verified:true};const invalid=scan.handlers.click();await tick();release();await invalid;if(results.children.length||!nodes.get('#remote-discovery-status').textContent.includes('could not'))throw Error('trusted discovery claim');
 })().catch(e=>{console.error(e);process.exit(1)});`)
}
