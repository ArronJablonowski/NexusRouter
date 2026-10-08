package webui

import (
	"strconv"
	"testing"
)

func TestRemoteEventsBrowserPaginationAndFailure(t *testing.T) {
	script := string(mustAsset(t, "assets/v1/remote-events.js"))
	runRoutingMapScript(t, `const vm=require('vm');function el(){return {children:[],handlers:{},hidden:false,setAttribute(){},replaceChildren(){this.children=[]},append(...x){this.children.push(...x)},addEventListener(k,f){this.handlers[k]=f}}};
 const parent=el(),document={createElement:el},window={NexusLive:{fetch:async()=>({ok:true,json:async()=>({version:1,csrf_token:'fresh'})})}};let calls=[],fail=false,malformed=false;
 const fetch=async(url,opts)=>{const b=JSON.parse(opts.body);calls.push(b);if(fail)return {ok:false};return {ok:true,json:async()=>({version:1,instance:'node-a',request_id:'request-existing-0001',task_id:'task-1',state:'running',from_sequence:b.after,next_sequence:b.after+1,head_sequence:2,has_more:b.after===0,events:[{sequence:malformed?9:b.after+1,kind:'<script>event</script>',time:'2026-10-01T00:00:00Z'}]})}};
 vm.runInNewContext(`+strconv.Quote(script)+`,{window,document,fetch});window.NexusRemoteEvents.attach(parent,'node-a','request-existing-0001','task-1','/app','csrf');
 const [title,refresh,next,status,list]=parent.children[0].children,tick=()=>new Promise(r=>setImmediate(r));
 (async()=>{if(calls.length||!next.hidden)throw Error('implicit load');refresh.handlers.click();refresh.handlers.click();await tick();if(calls.length!==1||next.hidden||list.children[0].textContent!=='1 · <script>event</script> · 2026-10-01T00:00:00Z')throw Error('first page');next.handlers.click();await tick();if(calls.length!==2||calls[1].after!==1||!next.hidden||list.children.length!==1)throw Error('pagination');fail=true;refresh.handlers.click();await tick();if(calls.length!==3||calls[2].after!==0||!next.hidden||list.children.length||!status.textContent.includes('unavailable'))throw Error('stale failure page');next.handlers.click();await tick();if(calls.length!==3)throw Error('automatic retry');fail=false;malformed=true;refresh.handlers.click();await tick();if(list.children.length||!next.hidden)throw Error('malformed sequence rendered');})().catch(e=>{console.error(e);process.exit(1)});`)
}
