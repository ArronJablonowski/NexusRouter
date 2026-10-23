"use strict";
// Presentation only: never sends requests, retries work or exposes reasoning.
window.NexusChatActivity=(()=>{
 const node=document.querySelector('#chat-activity'),label=document.querySelector('#chat-activity-label');
 const labels={sending:'Sending…',queued:'Waiting to start…',running:'Working…',writing:'Responding…',unknown:'Checking task status…',reconnecting:'Reconnecting…',offline:'Remote unavailable · checking status…'};
 let current='',owner='local',waiting=false,terminal=false;
 function render(){
  if(!node)return;
  const text=labels[current]?(waiting?'Waiting for your approval':labels[current]):'';
  node.hidden=!text;node.dataset.active=String(Boolean(text)&&!waiting&&['sending','queued','running','writing'].includes(current));
  if(label.textContent!==text)label.textContent=text;
 }
 function reset(){current='';owner='local';waiting=false;terminal=false;render();}
 function set(state,source='local'){
  if(source==='local'&&window.NexusHostMentions?.active?.())return;
  if(source!==owner){waiting=false;current='';terminal=false;owner=source;}
  if(terminal&&['offline','writing'].includes(state))return;
  terminal=['completed','succeeded','failed','canceled'].includes(state);
  if(state==='running'&&current==='writing')state='writing';
  current=Object.hasOwn(labels,state)?state:'';
  if(!current)waiting=false;
  render();
 }
 function connection(state='reconnecting'){if(labels[current])set(state,owner);}
 function controls(value){
  if(terminal||owner!=='local'||window.NexusHostMentions?.active?.())return;
  if(value.canCancel||value.canSteer){if(!current||['unknown','reconnecting'].includes(current))set('running');}
  else if(value.canResume||value.canFeedback)set('');
 }
 function approval(value){if(owner!=='local')return;waiting=Boolean(value);render();}
 return Object.freeze({set,reset,approval,connection,controls});
})();
