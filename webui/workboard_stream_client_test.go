package webui

import (
	"os/exec"
	"testing"
)

func TestEmbeddedWorkboardStreamFailsClosedAndRecovers(t *testing.T) {
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
function harness(EventSourceValue) {
  const notices = [], timers = [], liveStatus = {textContent:''}, stateNode = {}, window = {setTimeout(callback){timers.push(callback); return timers.length}}, base = '/app';
  const idPattern = /^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$/, states = ['backlog','ready','in_progress','blocked','review','done','canceled'];
  const boundedPrintable = value => typeof value === 'string' && value.length >= 1 && value.length <= 512 && !/[\u0000-\u001f\u007f-\u009f]/u.test(value);
  const notice = (node, message, failed) => notices.push({node,message,failed});
  const factorySource = "let boardSource = null, streamBoard = '', streamRevision = 0, streamFailures = 0, invalidationTimer = 0, selectedID = 'board-a';\n" +
    extract('closeBoardStream') + "\n" + extract('queueInvalidation') + "\n" + extract('validBoardEvent') + "\n" + extract('connectBoard') + "\n" +
    "return {connectBoard, state:()=>({boardSource,streamBoard,streamRevision,streamFailures,invalidationTimer})};";
  const factory = Function('EventSource','window','base','liveStatus','stateNode','idPattern','states','boundedPrintable','notice', factorySource);
  return Object.assign(factory(EventSourceValue,window,base,liveStatus,stateNode,idPattern,states,boundedPrintable,notice), {notices,timers,liveStatus});
}
let unavailable = harness(undefined); unavailable.connectBoard('board-a');
if (unavailable.state().boardSource || unavailable.state().streamBoard || !unavailable.liveStatus.textContent.includes('unavailable in this browser')) process.exit(1);
let thrown = harness(class { constructor(){throw new Error('blocked')} }); thrown.connectBoard('board-a');
if (thrown.state().boardSource || thrown.state().streamBoard || !thrown.liveStatus.textContent.includes('could not connect')) process.exit(2);
const sources = [];
class Source { constructor(url){this.url=url;this.listeners={};this.closed=false;sources.push(this)} addEventListener(kind,callback){this.listeners[kind]=callback} close(){this.closed=true} }
const run = harness(Source); run.connectBoard('board-a'); const first = sources[0];
if (!first || first.url !== '/app/api/v1/workboards/board-a/events' || run.state().streamBoard !== 'board-a') process.exit(3);
first.onopen(); if (run.state().streamFailures !== 0) process.exit(4);
const payload = {version:1,kind:'board.changed',durability:'committed',subject:'board-a',revision:2,cursor:'cursor-2',data:{board_id:'board-a',change:'card_changed',card_id:'card-a',state:'ready'}};
first.listeners['board.changed']({data:JSON.stringify(payload),lastEventId:'cursor-2'});
if (run.state().streamRevision !== 2 || run.timers.length !== 1) process.exit(5);
first.listeners['board.changed']({data:JSON.stringify(payload),lastEventId:'cursor-2'});
if (run.timers.length !== 1) process.exit(6);
for (let index=0; index<7; index++) first.onerror();
if (first.closed || run.state().streamFailures !== 7) process.exit(7);
first.onopen(); if (run.state().streamFailures !== 0) process.exit(8);
for (let index=0; index<8; index++) first.onerror();
if (!first.closed || run.state().boardSource || run.state().streamBoard) process.exit(9);
run.connectBoard('board-a'); const recovered = sources[1];
if (!recovered || recovered === first || run.state().boardSource !== recovered) process.exit(10);
first.onopen(); first.onerror(); first.listeners['board.changed']({data:JSON.stringify({...payload,revision:3,cursor:'cursor-3'}),lastEventId:'cursor-3'});
if (run.state().boardSource !== recovered || run.state().streamRevision !== 0 || run.state().streamFailures !== 0) process.exit(11);
recovered.listeners['board.changed']({data:'{',lastEventId:'cursor-x'});
if (!recovered.closed || run.state().boardSource || !run.notices.some(item => item.failed && item.message.includes('invalid data'))) process.exit(12);
run.connectBoard('board-a'); const mismatched = sources[2];
mismatched.listeners['board.changed']({data:JSON.stringify(payload),lastEventId:'wrong'});
if (!mismatched.closed || run.state().boardSource || !run.notices.some(item => item.failed && item.message.includes('could not be validated'))) process.exit(13);
run.connectBoard('bad/board');
if (!run.notices.some(item => item.failed && item.message.includes('address is invalid'))) process.exit(14);`
	command := exec.Command(node, "-e", script, "./assets/v1/workboards.js")
	command.Dir = "."
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("workboard stream browser model failed: %v\n%s", err, output)
	}
}
