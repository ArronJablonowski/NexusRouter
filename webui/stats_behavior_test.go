package webui

import (
	"strings"
	"testing"
)

func TestOdometerDistinguishesMissingPartialZeroAndLargeCounts(t *testing.T) {
	source := string(mustAsset(t, "assets/v1/stats.js"))
	start, end := strings.Index(source, "function node("), strings.Index(source, "function render(")
	if start < 0 || end < start {
		t.Fatal("stats function boundaries changed")
	}
	script := `const document={createElement(tag){return {tag,children:[],attributes:{},dataset:{},classList:{add(){}},setAttribute(k,v){this.attributes[k]=v},append(...nodes){this.children.push(...nodes)}}}};` + source[start:end] + `
 function check(count,want){const target=document.createElement('div');counters(target,count);const display=target.children[0].children[1];if(!display.attributes['aria-label'].includes(want))throw Error(JSON.stringify(display));return display;}
 check({input:'0',output:'0',unknown:2,measured:0,partial:0},'unavailable');
 check({input:'0',output:'0',unknown:0,measured:1,partial:0},'0 tokens');
 check({input:'38353',output:'541',unknown:2,measured:2,partial:2},'38,353 tokens');
 check({input:'9007199254740993',output:'0',unknown:0,measured:1,partial:0},'9,007,199,254,740,993 tokens');
 if(!coverage({unknown:2,partial:2,measured:2}).includes('actual usage may be higher'))throw Error('partial total presented as exact');
 if(!coverage({unknown:0,partial:0,measured:0}).includes('No usage recorded'))throw Error('empty period not distinguished');
 `
	runRoutingMapScript(t, script)
}
