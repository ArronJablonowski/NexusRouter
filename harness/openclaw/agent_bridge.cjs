// Private pinned plugin. Only the parent can register or authorize tool calls.
const fs = require('node:fs');
const http = require('node:http');
const path = require('node:path');
const config = JSON.parse(fs.readFileSync(path.join(__dirname, 'host.json'), 'utf8'));
let fault = false, ended = false, bytes = 0;
const seen = new Set();
function invoke(id, signal) {
  return new Promise((resolve, reject) => {
    const body = JSON.stringify({call_id:id});
    const req = http.request(config.url, {method:'POST', signal, timeout:config.timeout_ms,
      headers:{Authorization:'Bearer '+config.token,'Content-Type':'application/json','Content-Length':Buffer.byteLength(body)}}, res => {
      const chunks=[]; let size=0;
      res.on('data', chunk => {size+=chunk.length; if(size>8*1024*1024) req.destroy(new Error('oversized host response')); else chunks.push(chunk);});
      res.on('error', reject);
      res.on('end', () => {
        try {
          if(res.statusCode!==200 || (res.headers['content-type']||'').split(';')[0]!=='application/json') throw Error('host refused');
          const result=JSON.parse(Buffer.concat(chunks).toString('utf8'));
          if(Object.keys(result).sort().join(',')!=='content,end_tool_use,failed' || typeof result.content!=='string' || Buffer.byteLength(result.content)>1024*1024 || typeof result.failed!=='boolean' || typeof result.end_tool_use!=='boolean') throw Error('invalid host response');
          resolve(result);
        } catch(e) {reject(e);}
      });
    });
    req.on('error', reject); req.on('timeout',()=>req.destroy(new Error('host timeout'))); req.end(body);
  });
}
module.exports={id:'nexus-host',name:'NexusRouter host tools',register(api) {
  for (const tool of config.tools) api.registerTool({name:'nexus__'+tool.name,label:tool.name,description:tool.description,parameters:tool.parameters,
    async execute(id,args,signal) {
      try {
        if(fault || ended || typeof id!=='string' || !id.length || id.length>256 || seen.has(id) || seen.size>=128) throw Error('unbound tool call');
        seen.add(id);
        const result=await invoke(id,signal);
        if(fault) throw Error('prior host failure');
        ended ||= result.end_tool_use && !result.failed;
        const receipt=JSON.stringify({id,name:tool.name,args,...result})+'\n';
        bytes+=Buffer.byteLength(receipt); if(bytes>8*1024*1024) throw Error('receipt limit');
        fs.appendFileSync(config.receipts,receipt,{mode:0o600});
        return {content:[{type:'text',text:result.content}],details:{status:result.failed?'error':'completed'}};
      } catch(e) {fault=true; throw Error('NexusRouter host tool unavailable; inspect canonical journal');}
    }
  });
}};
