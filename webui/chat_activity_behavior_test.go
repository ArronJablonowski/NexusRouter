package webui

import "testing"

func TestChatActivityLifecycleAndRemoteIsolation(t *testing.T) {
	source := string(mustAsset(t, "assets/v1/chat-activity.js"))
	runRoutingMapScript(t, `const label={textContent:''},node={hidden:true,dataset:{}},document={querySelector:s=>s==='#chat-activity'?node:label},window={};`+source+`
 const a=window.NexusChatActivity,expect=(text,active)=>{if(node.hidden!==!text||label.textContent!==text||node.dataset.active!==String(active))throw Error(JSON.stringify({text:label.textContent,hidden:node.hidden,active:node.dataset.active}));};
 a.set('queued');expect('Waiting to start…',true);a.set('running');expect('Working…',true);a.set('writing');a.set('running');expect('Responding…',true);
 a.approval(true);expect('Waiting for your approval',false);a.approval(false);expect('Responding…',true);
 a.connection();expect('Reconnecting…',false);a.set('running');expect('Working…',true);
 for(const state of ['completed','failed','canceled','succeeded']){a.set('running');a.set(state);expect('',false);a.connection();expect('',false);a.set('writing');expect('',false);}
 a.controls({canCancel:true});expect('',false);a.reset();a.controls({canCancel:true});expect('Working…',true);a.controls({canResume:true});expect('',false);
 window.NexusHostMentions={active:()=>true};a.set('running','remote');a.set('completed');expect('Working…',true);a.set('offline','remote');expect('Remote unavailable · checking status…',false);a.set('succeeded','remote');expect('',false);a.set('offline','remote');expect('',false);a.reset();expect('',false);
 `)
}
