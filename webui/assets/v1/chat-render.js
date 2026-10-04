"use strict";
window.NexusChatRender = (() => {
 function preserve(root, change) {
  const top = root.scrollTop, bottom = root.children.length > 0 && root.scrollHeight - root.clientHeight - top < 24;
  const edge = root.getBoundingClientRect().top;
  const anchor = [...root.children].find(node => node.getBoundingClientRect().bottom > edge);
  const offset = anchor ? anchor.getBoundingClientRect().top : 0;
  const panel = root.closest('.transcript-panel'), panelTop = panel && panel.scrollTop;
  change();
  if (bottom) root.scrollTop = root.scrollHeight;
  else root.scrollTop = anchor && anchor.isConnected ? top + anchor.getBoundingClientRect().top - offset : top;
  if (panel) panel.scrollTop = panelTop;
 }
 function messages(root, items, reset) {
  preserve(root, () => {
   const existing = new Map([...root.children].map(node => [node.dataset.messageKey, node]));
   const keep = new Set();
   let cursor = reset ? root.firstElementChild : null;
   for (const item of items) {
    if (!item || !['user','assistant'].includes(item.role) || typeof item.text !== 'string' || item.text.length > (1 << 20)) continue;
    const key = item.id || item.role + ':' + item.text;
    let row = existing.get(key);
    if (!row) {row=document.createElement('li');row.className='message '+item.role;row.dataset.messageKey=key;const label=document.createElement('span');label.className='message-label';label.textContent=item.role;row.append(label,document.createElement('p'));}
    const content=row.querySelector('p');if(content.textContent!==item.text)content.textContent=item.text;
    keep.add(row);
    if (reset) {if(row!==cursor)root.insertBefore(row,cursor);cursor=row.nextElementSibling;}
    else if(!row.isConnected)root.append(row);
   }
   if(reset)for(const node of [...root.children])if(!keep.has(node))node.remove();
  });
 }
 return {messages,preserve};
})();
