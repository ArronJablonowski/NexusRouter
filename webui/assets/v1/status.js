"use strict";
(() => {
 const base=document.body.dataset.basePath||"";
 if(window.location.pathname!==base+"/status")return;
 document.querySelector("#chat-view").hidden=true;
 document.querySelector("#status-view").hidden=false;
 document.title="Status · NexusRouter";
 const task=new URLSearchParams(window.location.search).get("task");
 function load(){return Promise.all([window.NexusInspector.loadGlobals(),task&&/^[A-Za-z0-9_-]{1,128}$/.test(task)?window.NexusInspector.loadTask(task):Promise.resolve()]);}
 window.NexusLive.watch("status",load,{interval:5000});load();
})();
