package webui

import (
	"strconv"
	"testing"
)

func TestModelUseOldRefreshCannotOverwriteSavedToggle(t *testing.T) {
	script := string(mustAsset(t, "assets/v1/model-use.js"))
	runRoutingMapScript(t, `const vm=require('vm');function el(){return {children:[],handlers:{},isConnected:true,disabled:false,textContent:'',classList:{add(){}},setAttribute(){},append(...x){this.children.push(...x)},prepend(...x){this.children.unshift(...x)},addEventListener(k,f){this.handlers[k]=f}}};
 const root=el(),document={body:{dataset:{basePath:'/app'}},createElement:el},location={pathname:'/app/models'};let refresh,hold=false,release,stored={version:1,disabled:{}};
 const window={NexusLive:{watch(name,fn){refresh=fn},fetch:async(url,opts)=>{
  if(url.endsWith('/session/csrf'))return {ok:true,json:async()=>({version:1,csrf_token:'fresh'})};
  const body=JSON.parse(opts.body);if(body.model){stored={version:1,disabled:body.enabled?{}:{'local/m':true}};return {ok:true,json:async()=>stored};}
  const snapshot=JSON.parse(JSON.stringify(stored));if(hold)return new Promise(resolve=>release=()=>resolve({ok:true,json:async()=>snapshot}));
  return {ok:true,json:async()=>snapshot};
 }}};vm.runInNewContext(`+strconv.Quote(script)+`,{window,document,location});window.NexusModelUse.attach(root,'local','m');
 const input=root.children[0].children[0].children[0],tick=()=>new Promise(r=>setImmediate(r));
 (async()=>{await tick();if(!input.checked)throw Error('initial policy absent');hold=true;const pending=refresh();await tick();input.checked=false;await input.handlers.change();if(input.checked)throw Error('toggle save failed');release();await pending;if(input.checked||!stored.disabled['local/m'])throw Error('old refresh overwrote confirmed policy');})().catch(e=>{console.error(e);process.exit(1)});`)
}
