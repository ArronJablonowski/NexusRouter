package webui

import (
	"os/exec"
	"testing"
)

func TestEmbeddedWorkboardLifecycleSnapshotValidationAndRendering(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is unavailable")
	}
	script := `'use strict';
const fs = require('fs'), source = fs.readFileSync(process.argv[1], 'utf8');
function extract(name) {
  const start = source.indexOf('function ' + name + '('); if (start < 0) process.exit(40);
  const open = source.indexOf('{', start); let depth = 0;
  for (let index = open; index < source.length; index++) { if (source[index] === '{') depth++; else if (source[index] === '}' && --depth === 0) return source.slice(start, index + 1); }
  process.exit(41);
}
const names = ['textBytes','validUnicode','boundedText','validTime','uniqueIDs','validBudget','validCriteria','optionalID','validClaim','validCandidate','validEvidence','validAttempt','validAcceptance','validLifecycle','validLifecycleBatch','cardLifecycleSummary'];
const prelude = "const idPattern=/^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$/; const validatorPattern=/^[A-Za-z0-9](?:[A-Za-z0-9_-]*[A-Za-z0-9])?(?:\\.[A-Za-z0-9](?:[A-Za-z0-9_-]*[A-Za-z0-9])?)*$/; const digestPattern=/^[0-9a-f]{64}$/; const attemptStates=['running','review','accepted','rejected','failed','canceled']; const lifecycleByCard=new Map();\n";
const api = Function(prelude + names.map(extract).join('\n') + '\nreturn {validLifecycleBatch,cardLifecycleSummary,lifecycleByCard};')();
const digest = character => character.repeat(64), now = '2026-09-10T12:00:00Z', later = '2026-09-10T12:01:00Z';
const criterion = {version:1,id:'tests',kind:'objective',required_source:'deterministic',validator_id:'validator-a',description:'Tests pass',required:true};
function card(id, state='blocked') { return {id,board_id:'board-a',state,attempt_count:1,current_attempt_id:'attempt-'+id,current_claim_id:state==='blocked'?'claim-'+id:'',acceptance_id:'',criteria_revision:2,criteria:[criterion]}; }
function running(card) { return {version:1,card_id:card.id,attempt:{version:1,id:card.current_attempt_id,board_id:'board-a',card_id:card.id,ordinal:1,revision:2,state:'running',worker_id:'worker-a',criteria_revision:1,criteria_digest:digest('a'),policy_digest:digest('b'),budget:{attempt_limit:3,time_limit_ms:1000,token_limit:100,cost_micros:100},criteria:[criterion],task_ids:[],session_ids:[],claim:{version:1,id:card.current_claim_id,board_id:'board-a',card_id:card.id,attempt_id:card.current_attempt_id,revision:2,state:'attention',owner_id:'worker-a',owner_type:'worker',expires_at:later,last_heartbeat:now},evidence:[],started_at:now},checkpoints:[],checkpoint_count:0,checkpoints_has_more:false}; }
const one = card('card-a'), lifecycle = running(one), snapshot = {board:{id:'board-a'},cards:[one],lifecycle:[lifecycle]};
let page = api.validLifecycleBatch(snapshot,true);
if (!(page instanceof Map) || page.get('card-a') !== lifecycle) process.exit(1);
if (api.validLifecycleBatch({...snapshot,lifecycle:[lifecycle,lifecycle]},true)) process.exit(2);
if (api.validLifecycleBatch({...snapshot,lifecycle:[{...lifecycle,card_id:'card-b'}]},true)) process.exit(3);
if (api.validLifecycleBatch({...snapshot,lifecycle:[{...lifecycle,attempt:{...lifecycle.attempt,id:'attempt-other'}}]},true)) process.exit(4);
api.lifecycleByCard.set('card-a',lifecycle);
if (api.validLifecycleBatch(snapshot,false)) process.exit(5);
const second = card('card-b'), secondLifecycle = running(second);
page = api.validLifecycleBatch({board:{id:'board-a'},cards:[second],lifecycle:[secondLifecycle]},false);
if (!(page instanceof Map) || !page.has('card-b')) process.exit(6);
const summary = api.cardLifecycleSummary(one,lifecycle);
if (summary.criteria !== 'Criteria revision 2 · 1 required of 1' || summary.attempt !== 'Attempt 1 · running · criteria revision 1' || !summary.attention || !summary.claim.includes('Claim owner worker-a · attention') || !summary.claim.includes('stale/orphan attention required')) process.exit(7);
const ready = card('card-c','ready'), rejection = running({...ready,current_claim_id:'claim-card-c'}), attempt = rejection.attempt;
attempt.state='rejected'; attempt.ended_at=later; attempt.claim.state='released'; attempt.claim.released_at=later;
attempt.candidate={version:1,id:'candidate-a',board_id:'board-a',card_id:ready.id,attempt_id:attempt.id,revision:1,digest:digest('c'),criteria_digest:attempt.criteria_digest,policy_digest:attempt.policy_digest,evidence_digest:digest('d'),evidence_count:1,summary:'Candidate',artifact_refs:[],submitted_by:'worker-a',created_at:now};
attempt.evidence=[{version:1,id:'evidence-a',revision:1,board_id:'board-a',card_id:ready.id,attempt_id:attempt.id,candidate_id:'candidate-a',criterion_id:'tests',source:'deterministic',outcome:'failed',actor_id:'validator-a',actor_type:'validator',reference:'test-run-a',candidate_digest:digest('c'),criteria_digest:attempt.criteria_digest,policy_digest:attempt.policy_digest,created_at:now}];
attempt.acceptance_id='acceptance-a'; attempt.decision_by='operator-a'; attempt.decision_by_type='operator'; attempt.decision_authority_id='browser-policy'; attempt.acceptance_evidence_digest=digest('e');
rejection.acceptance={version:1,id:'acceptance-a',board_id:'board-a',card_id:ready.id,attempt_id:attempt.id,candidate_id:'candidate-a',candidate_digest:digest('c'),criteria_revision:1,criteria_digest:digest('a'),prior_evidence_head_revision:1,prior_evidence_set_digest:digest('e'),evidence_head_revision:1,evidence_set_digest:digest('e'),policy_digest:digest('b'),decision:'rejected',decided_by:'operator-a',decided_by_type:'operator',decision_authority_id:'browser-policy',rationale:'Evidence did not pass.',decided_at:later};
if (!api.validLifecycleBatch({board:{id:'board-a'},cards:[ready],lifecycle:[rejection]},true) || api.validLifecycleBatch({board:{id:'board-a'},cards:[ready],lifecycle:[{...rejection,acceptance:{...rejection.acceptance,decided_by:'worker-a'}}]},true)) process.exit(10);
attempt.evidence.push({...attempt.evidence[0],id:'evidence-model',revision:2,source:'model_audit',outcome:'passed',actor_id:'reviewer-model',actor_type:'model',reference:'advisory-only'});
const rejectionSummary = api.cardLifecycleSummary(ready,rejection).acceptance;
if (!rejectionSummary.includes('Acceptance rejected · decided by operator-a') || !rejectionSummary.includes('reason Evidence did not pass.') || !rejectionSummary.includes('evidence references test-run-a') || rejectionSummary.includes('advisory-only')) process.exit(11);
const subjectiveCriterion = {version:1,id:'taste',kind:'subjective',required_source:'user_feedback',validator_id:'operator-review',description:'User approves the result',required:true};
const subjectiveCard = card('card-subjective','review'); subjectiveCard.current_claim_id=''; subjectiveCard.criteria=[subjectiveCriterion];
const subjective = running({...subjectiveCard,current_claim_id:'claim-subjective'}), subjectiveAttempt = subjective.attempt;
subjectiveAttempt.state='review'; subjectiveAttempt.ended_at=later; subjectiveAttempt.claim.state='released'; subjectiveAttempt.claim.released_at=later; subjectiveAttempt.criteria=[subjectiveCriterion]; subjectiveAttempt.evidence=[];
subjectiveAttempt.candidate={version:1,id:'candidate-subjective',board_id:'board-a',card_id:subjectiveCard.id,attempt_id:subjectiveAttempt.id,revision:1,digest:digest('f'),criteria_digest:subjectiveAttempt.criteria_digest,policy_digest:subjectiveAttempt.policy_digest,evidence_digest:digest('0'),evidence_count:0,summary:'Creative candidate',artifact_refs:[],submitted_by:'worker-a',created_at:now};
if (!api.validLifecycleBatch({board:{id:'board-a'},cards:[subjectiveCard],lifecycle:[subjective]},true)) process.exit(12);
subjectiveAttempt.state='accepted'; subjectiveAttempt.evidence=[{version:1,id:'feedback-a',revision:1,board_id:'board-a',card_id:subjectiveCard.id,attempt_id:subjectiveAttempt.id,candidate_id:'candidate-subjective',criterion_id:'taste',source:'user_feedback',outcome:'passed',actor_id:'operator-a',actor_type:'operator',reference:'browser-review',candidate_digest:digest('f'),criteria_digest:subjectiveAttempt.criteria_digest,policy_digest:subjectiveAttempt.policy_digest,created_at:later}];
subjectiveAttempt.acceptance_id='acceptance-subjective'; subjectiveAttempt.decision_by='operator-a'; subjectiveAttempt.decision_by_type='operator'; subjectiveAttempt.decision_authority_id='browser-policy'; subjectiveAttempt.acceptance_evidence_digest=digest('1');
subjective.acceptance={version:1,id:'acceptance-subjective',board_id:'board-a',card_id:subjectiveCard.id,attempt_id:subjectiveAttempt.id,candidate_id:'candidate-subjective',candidate_digest:digest('f'),criteria_revision:1,criteria_digest:subjectiveAttempt.criteria_digest,prior_evidence_head_revision:0,prior_evidence_set_digest:digest('0'),evidence_head_revision:1,evidence_set_digest:digest('1'),policy_digest:subjectiveAttempt.policy_digest,decision:'accepted',decided_by:'operator-a',decided_by_type:'operator',decision_authority_id:'browser-policy',rationale:'The authenticated user approves.',decided_at:later};
const acceptedSubjectiveCard = {...subjectiveCard,state:'done',acceptance_id:'acceptance-subjective'};
if (!api.validLifecycleBatch({board:{id:'board-a'},cards:[acceptedSubjectiveCard],lifecycle:[subjective]},true) || api.validLifecycleBatch({board:{id:'board-a'},cards:[acceptedSubjectiveCard],lifecycle:[{...subjective,acceptance:{...subjective.acceptance,prior_evidence_head_revision:-1}}]},true)) process.exit(13);

const nodes = [];
function make(tag) { const node = {tag,className:'',textContent:'',children:[],dataset:{},hidden:false,disabled:false,append(...items){this.children.push(...items)},setAttribute(){},addEventListener(){}}; nodes.push(node); return node; }
const document = {createElement:make}, window = {dispatchEvent(){}};
const element = Function('document','return (' + extract('element') + ')')(document);
const cardNode = Function('element','cardLifecycleSummary','window','document','idPattern','return (' + extract('cardNode') + ')')(element,api.cardLifecycleSummary,window,document,/^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$/);
one.title='Blocked work'; one.priority='high'; one.remaining_dependencies=0; one.assignee_id='worker-a'; one.pause_requested=false; one.cancel_requested=false; one.block_reason='waiting_for_input'; one.description=''; one.labels=[];
const rendered = cardNode(one,lifecycle), text = function flatten(node){ return [node.textContent,...node.children.flatMap(flatten)].join('\n'); }(rendered);
for (const expected of ['blocked: waiting_for_input','Block reason: waiting_for_input','Required: Tests pass','Attempt 1 · running · criteria revision 1','Claim owner worker-a · attention','stale/orphan claim attention']) if (!text.includes(expected)) process.exit(8);
if (!nodes.some(node => node.className === 'card-alert' && node.textContent.includes('stale/orphan'))) process.exit(9);`
	command := exec.Command(node, "-e", script, "./assets/v1/workboards.js")
	command.Dir = "."
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("workboard lifecycle browser model failed: %v\n%s", err, output)
	}
}
