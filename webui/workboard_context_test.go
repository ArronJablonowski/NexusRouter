package webui

import (
	"os/exec"
	"testing"
)

func TestEmbeddedWorkboardRestoresSelectedExpandedCardContext(t *testing.T) {
	nodeBinary, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is unavailable")
	}
	script := `'use strict';
const fs = require('fs'), source = fs.readFileSync(process.argv[1], 'utf8'), client = require(process.argv[2]);
function extract(name) {
  const start = source.indexOf('function ' + name + '('); if (start < 0) process.exit(20);
  const open = source.indexOf('{', start); let depth = 0;
  for (let index = open; index < source.length; index++) { if (source[index] === '{') depth++; else if (source[index] === '}' && --depth === 0) return source.slice(start, index + 1); }
  process.exit(21);
}
const cardContext = Function('return (' + extract('cardContext') + ')')();
function fixture(transition) {
  const card = transition && transition.card, attributes = {}, classes = new Set(), details = {hidden:true,dataset:{},focus(){focuses++}}, toggle = {setAttribute(name,value){attributes[name]=value},focus(){focuses++}};
  const item = {classList:{add(name){classes.add(name)}},querySelector(selector){return selector === '.card-toggle' ? toggle : selector === '.card-details' ? details : null},focus(){focuses++}};
  let loads = 0, focuses = 0;
  const restore = Function('selectedCardAnchor','cardNodes','cardContext','loadCardDetails','let selectedCard=null; return (' + extract('restoreCardView') + ')')(
    transition && transition.anchor, new Map(card ? [[card.id,item]] : []), cardContext, () => {loads++});
  return {result:restore(card),attributes,classes,details,loads,focuses};
}
const card = {board_id:'board-a',id:'card-a',labels:['runtime'],dependencies:['card-b'],criteria:[{id:'tests'}],budget:{attempt_limit:1}}, anchor = client.cardViewAnchor('board-a','card-a',true);
if (!anchor || !Object.isFrozen(anchor) || client.cardViewAnchor('bad/board','card-a',true) || client.cardViewAnchor('board-a','card-a','true')) process.exit(1);
const partial = client.cardViewTransition(anchor,'board-a','board-a',[],false);
if (!partial || partial.anchor.cardID !== 'card-a' || partial.card !== null || !Object.isFrozen(partial) || !Object.isFrozen(partial.anchor)) process.exit(2);
const superseded = client.cardViewTransition(partial.anchor,'board-a','board-a',[],false);
if (!superseded || superseded.anchor.cardID !== 'card-a' || superseded.card !== null) process.exit(3);
const filtered = client.cardViewTransition(superseded.anchor,'board-a','board-a',[],false);
if (!filtered || filtered.anchor.cardID !== 'card-a' || filtered.card !== null) process.exit(4);
const later = client.cardViewTransition(filtered.anchor,'board-a','board-a',[card],false);
if (!later || later.anchor.cardID !== 'card-a' || later.card !== card) process.exit(5);
const completePresent = client.cardViewTransition(later.anchor,'board-a','board-a',[card],true), deleted = client.cardViewTransition(anchor,'board-a','board-a',[],true), switched = client.cardViewTransition(anchor,'board-a','board-b',[],false);
if (!completePresent || completePresent.card !== card || !completePresent.anchor || !deleted || deleted.anchor !== null || deleted.card !== null || !switched || switched.anchor !== null || switched.card !== null) process.exit(6);
const oversized = Array.from({length:10001}, (_, index) => ({board_id:'board-a',id:'card-'+String(index)}));
if (client.cardViewTransition({...anchor,extra:true},'board-a','board-a',[card],false) || client.cardViewTransition(anchor,'board-a','board-a',[card,card],false) || client.cardViewTransition(anchor,'board-a','board-a',[card,{board_id:'board-a',id:'bad/card'}],false) || client.cardViewTransition(anchor,'board-a','board-a',[{...card,board_id:'board-b'}],false) || client.cardViewTransition(anchor,'board-a','board-a',oversized,false)) process.exit(7);
let state = fixture(later);
if (!state.result || !Object.isFrozen(state.result) || state.result === card || !state.classes.has('selected-card') || state.attributes['aria-pressed'] !== 'true' || state.attributes['aria-expanded'] !== 'true' || state.details.hidden || state.details.dataset.loaded !== 'true' || state.loads !== 1 || state.focuses !== 0) process.exit(8);
const collapsed = client.cardViewTransition(client.cardViewAnchor('board-a','card-a',false),'board-a','board-a',[card],false); state = fixture(collapsed);
if (!state.result || state.attributes['aria-pressed'] !== 'true' || state.attributes['aria-expanded'] !== 'false' || !state.details.hidden || state.loads !== 0 || state.focuses !== 0) process.exit(9);
state = fixture(partial);
if (state.result !== null || state.loads !== 0 || state.classes.size !== 0 || state.focuses !== 0) process.exit(10);`
	command := exec.Command(nodeBinary, "-e", script, "./assets/v1/workboards.js", "./assets/v1/workboard-client.js")
	command.Dir = "."
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("selected workboard card context restoration failed: %v\n%s", err, output)
	}
}
