package webui

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestChromeSharedStyleAcrossPages(t *testing.T) {
	chrome, node := chromeTestBinary(t), chromeTestNode(t)
	shell, err := NewShellHandler(ShellOptions{BasePath: "/app", HostAllowed: func(string) bool { return true }, Authenticated: func(*http.Request) bool { return true }})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Unavailable data must retain a usable, readable shell, without mutations.
		if strings.HasPrefix(r.URL.Path, "/app/api/") {
			http.Error(w, "fixture unavailable", 503)
			return
		}
		shell.ServeHTTP(w, r)
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	port, stop := startChromeForTest(t, ctx, chrome)
	defer stop()
	script := strings.Split(chromeRemoteDiscoveryCDP, "await cdp('Page.navigate'")[0] + `
await cdp('Emulation.setEmulatedMedia',{features:[{name:'prefers-reduced-motion',value:'reduce'}]});
await cdp('Page.navigate',{url:origin+'/app/chats'});
await eventually('document.querySelector("[data-view=chats]")?.getAttribute("aria-current")==="page"','Chat shell missing');
const pages=await evaluate('[...document.querySelectorAll(".rail a")].map(a=>({view:a.dataset.view,url:a.href}))');
for(const page of pages){
 await cdp('Page.navigate',{url:page.url});
 await eventually('document.querySelector("[data-view='+page.view+']")?.getAttribute("aria-current")==="page"','Page not selected '+page.view);
 if(page.view==='settings')await eventually('document.querySelector("#cleanup-last").textContent==="Last cleanup unavailable."','cleanup loading state never settled');
 for(const width of [1440,1024,768,390,320]){
  await cdp('Emulation.setDeviceMetricsOverride',{width,height:1000,deviceScaleFactor:1,mobile:false});
  await cdp('Input.dispatchKeyEvent',{type:'keyDown',key:'Tab',code:'Tab',windowsVirtualKeyCode:9});
  await cdp('Input.dispatchKeyEvent',{type:'keyUp',key:'Tab',code:'Tab',windowsVirtualKeyCode:9});
  const errors=await evaluate(` + "`" + `(()=>{
   const errors=[],main=document.querySelector('main'),visible=n=>n.getClientRects().length>0;
   const rgb=color=>color.match(/[0-9.]+/g)?.map(Number),lum=c=>c.slice(0,3).map(v=>{v/=255;return v<=.04045?v/12.92:Math.pow((v+.055)/1.055,2.4)}).reduce((sum,v,i)=>sum+v*[.2126,.7152,.0722][i],0);
   const contrast=(a,b)=>(Math.max(lum(a),lum(b))+.05)/(Math.min(lum(a),lum(b))+.05);
   if(document.documentElement.scrollWidth>innerWidth)errors.push('page overflow');
   for(const b of main.querySelectorAll('button')){
    if(!visible(b))continue;const r=b.getBoundingClientRect();
    if(!b.textContent.trim()&&!b.getAttribute('aria-label'))errors.push('unnamed button '+b.id);
    if(r.width>innerWidth||r.height<24)errors.push('button dimensions '+b.id);
    const style=getComputedStyle(b),fg=rgb(style.color),bg=rgb(style.backgroundColor);
    if(!b.disabled&&fg&&bg&&(fg.length===3||fg[3]===1)&&(bg.length===3||bg[3]===1)&&contrast(fg,bg)<4.5)errors.push('button contrast '+b.id);
   }
   for(const input of main.querySelectorAll('input:not([type=checkbox]):not([type=radio]):not([type=file]),textarea,select')){
    if(!visible(input)||input.disabled)continue;const style=getComputedStyle(input),border=rgb(style.borderTopColor),bg=rgb(style.backgroundColor);
    if(parseFloat(style.borderTopWidth)>0&&border&&bg&&(bg.length===3||bg[3]===1)&&contrast(border,bg)<3)errors.push('field boundary contrast '+input.id);
   }
   for(const n of main.querySelectorAll('[hidden]'))if(visible(n))errors.push('hidden control exposed '+n.id+' '+n.className);
   const primary=[...main.children].filter(n=>n.tagName==='SECTION'&&visible(n));
   if(primary.length!==1)errors.push('page visibility');
   const button=[...main.querySelectorAll('button')].find(b=>visible(b)&&!b.disabled);
   if(button){button.focus();if(!button.matches(':focus-visible')||getComputedStyle(button).outlineStyle==='none')errors.push('keyboard focus '+button.id+' active='+document.activeElement.id+' visible='+button.matches(':focus-visible')+' outline='+getComputedStyle(button).outlineStyle+' hasFocus='+document.hasFocus());if(parseFloat(getComputedStyle(button).transitionDuration)!==0)errors.push('reduced motion');}
   return errors;
  })()` + "`" + `);
  if(errors.length)throw Error(page.view+' '+width+': '+errors.join(', '));
  if(process.env.NEXUS_UI_QA_SCREENSHOTS&&[1440,390].includes(width)){
   const fs=await import('node:fs');fs.mkdirSync(process.env.NEXUS_UI_QA_SCREENSHOTS,{recursive:true});
   const shot=await cdp('Page.captureScreenshot',{format:'png'});fs.writeFileSync(process.env.NEXUS_UI_QA_SCREENSHOTS+'/'+page.view+'-'+width+'.png',Buffer.from(shot.data,'base64'));
  }
 }
}
await cdp('Page.reload');await eventually('document.querySelector("[data-view=settings]")?.getAttribute("aria-current")==="page"','Reload lost page');
socket.close();`
	out, err := exec.CommandContext(ctx, node, "-e", script, strconv.Itoa(port), server.URL).CombinedOutput()
	if err != nil {
		t.Fatalf("%v %s", err, out)
	}
}
