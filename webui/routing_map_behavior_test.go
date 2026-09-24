package webui

import (
	"os/exec"
	"strings"
	"testing"
)

func routingMapSection(t *testing.T, start, end string) string {
	t.Helper()
	script := string(mustAsset(t, "assets/v1/routing-map.js"))
	a, b := strings.Index(script, start), strings.Index(script, end)
	if a < 0 || b <= a {
		t.Fatal("routing map function boundaries changed")
	}
	return script[a:b]
}

func runRoutingMapScript(t *testing.T, script string) {
	t.Helper()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is unavailable")
	}
	if out, err := exec.Command(node, "-e", script).CombinedOutput(); err != nil {
		t.Fatalf("routing map regression: %v\n%s", err, out)
	}
}

func TestRoutingMapUsesExactDirectEvidenceBeforeConfiguredPrior(t *testing.T) {
	script := `let snapshot={models:[{id:'a',usable:true,locality:'local',capabilities:['code']},{id:'b',usable:true,locality:'local',capabilities:['code']}],fitness:[
{model_id:'a',domain:'coding',profile:'benchmark',score:.9,samples:20,fallback_eligible:true},
{model_id:'a',domain:'code',profile:'default',score:.1,samples:20},
{model_id:'b',domain:'code',profile:'default',score:.8,samples:20}],
evidence_fallbacks:[{domain:'code',profile:'default',source_domain:'coding',source_profile:'benchmark'}],specialists_allow_cloud:false};
const job={key:'coding',domain:'code',capabilities:['code']};
` + routingMapSection(t, "function learned(", "function commander(") + `
if (learned(snapshot.models[0],job).score !== .1 || ranked(job)[0].id !== 'b') throw Error('benchmark prior overrode direct rejection');
snapshot.models[1].context_selection_status='blocked';
if(ranked(job).some(model=>model.id==='b'))throw Error('unsafe context was ranked as eligible');
delete snapshot.models[1].context_selection_status;
snapshot.fitness.splice(1,1);
if (learned(snapshot.models[0],job).score !== .9) throw Error('configured prior was not used');
snapshot.fitness[0].fallback_eligible=false;
if (learned(snapshot.models[0],job)!==null) throw Error('mixed or unverified judge evidence transferred');
snapshot.fitness[0].fallback_eligible=true;
snapshot.evidence_fallbacks=[];
if (learned(snapshot.models[0],job) !== null) throw Error('unconfigured alias/profile evidence transferred');
snapshot.evidence_fallbacks=[{domain:'code',profile:'default',source_domain:'missing',source_profile:'default'},{domain:'missing',profile:'default',source_domain:'coding',source_profile:'benchmark'}];
if (learned(snapshot.models[0],job) !== null) throw Error('fallback mapping chained');
snapshot.evidence_fallbacks=[{domain:'code',profile:'benchmark',source_domain:'coding',source_profile:'benchmark'}];
if (learned(snapshot.models[0],job) !== null) throw Error('target profile ignored');
`
	runRoutingMapScript(t, script)
}

func TestRoutingMapIgnoresStaleRefreshSuccessAndFailure(t *testing.T) {
	function := routingMapSection(t, "async function loadRouting()", "async function report(")
	script := `const vm=require('vm');
const source=` + "`" + function + "`" + `;
async function scenario(failOld) {
  const nodes=new Map(); const document={hidden:false,querySelector(q){if(!nodes.has(q))nodes.set(q,{textContent:'',hidden:false,clears:0,replaceChildren(){this.clears++},append(){}});return nodes.get(q)}};
  let resolveOld,rejectOld,resolveNew,calls=0,schedules=0;
  const scope={document,window:{clearTimeout(){},setTimeout(){schedules++;return 1}},routingTimer:0,routingGeneration:0,snapshot:null,commander:()=>null,ranked:()=>[],drawBranches(){},jobs:[],creativePreference:'',inventory(){return new Promise((resolve,reject)=>{if(calls++===0){resolveOld=resolve;rejectOld=reject}else resolveNew=resolve})}};
  vm.createContext(scope);vm.runInContext(source,scope);
  const old=scope.loadRouting(),fresh=scope.loadRouting();
  resolveNew({models:[{},{}],fitness:[],local_concurrency:'1'});await fresh;
  const status=document.querySelector('#routing-live-status').textContent,clears=document.querySelector('#specialist-grid').clears;
  if (!status.includes('2 models')) throw Error('newest snapshot not rendered');
  if(failOld)rejectOld(Error('old failure'));else resolveOld({models:[{}],fitness:[],local_concurrency:'1'});
  await old;
  if(document.querySelector('#routing-live-status').textContent!==status || document.querySelector('#specialist-grid').clears!==clears || scope.snapshot.models.length!==2 || schedules!==1) throw Error('stale refresh changed current display or timer');
}
(async()=>{await scenario(false);await scenario(true)})().catch(err=>{console.error(err);process.exit(1)});`
	runRoutingMapScript(t, script)
}

func TestRoutingMapKeepsExpandedModelWhenCardsRebuild(t *testing.T) {
	script := `const expandedModels=new Set();let savedURL='';
const window={location:{href:'http://127.0.0.1/app/routing-map'},history:{replaceState(a,b,url){savedURL=String(url)}}};
function element(name,className,value){return {name,className,textContent:value,children:[],open:false,handlers:{},append(...values){this.children.push(...values)},addEventListener(name,fn){this.handlers[name]=fn}}}
function learned(){return null}
` + routingMapSection(t, "function contextLabel(", "function circuitPath(") + `
const model={id:'actual-model',model:'Actual model',provider:'local',health:'healthy',locality:'local',capabilities:['code'],context_tokens:131072,selected_context_tokens:32768};
const job={key:'coding',domain:'code'};
let card=modelChip(model,0,job),details=card.children[1];
details.children[0].handlers.click(); details.open=true;
if(!new URL(savedURL).searchParams.getAll('expanded').includes('coding|actual-model'))throw Error('expansion was not persisted');
card=modelChip(model,2,job);details=card.children[1];
if(!details.open)throw Error('expanded model collapsed during reranking');
details.children[0].handlers.click();details.open=false;
if(modelChip(model,0,job).children[1].open)throw Error('collapsed model reopened after refresh');
`
	runRoutingMapScript(t, script)
}
