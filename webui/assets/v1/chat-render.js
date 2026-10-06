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
	function approval(approvalList, item, {element,stateLabel,decideApproval,openApproval}) {
  let row = [...approvalList.children].find(node => node.dataset.approvalId === item.id);
  if (!row) {
   row = element("li", "message approval-message"); row.dataset.approvalId = item.id;
   row.append(element("span", "message-label", "Tool approval"), element("p", "approval-request"), element("p", "approval-scope"), element("pre", "approval-proposal"), element("p", "approval-result"));
   const actions = element("div", "control-actions");
   for (const [action, label] of [["allow", "Allow once"], ["deny", "Deny"], ["revoke", "Revoke approval"]]) {
    const button = element("button", action === "allow" ? "" : "secondary", label);
    button.type = "button"; button.dataset.approvalAction = action;
    button.addEventListener("click", () => decideApproval(action, row.approvalItem)); actions.append(button);
   }
   const details = element("button", "secondary", "View request details"); details.type="button";
   details.addEventListener("click", () => openApproval(row.approvalItem, details)); actions.append(details);
   row.append(actions); approvalList.append(row);
  }
  row.approvalItem = item;
  const set = (selector, value) => { const node = row.querySelector(selector); if(node.textContent !== value) node.textContent=value; };
  set(".approval-request", item.toolName + " · " + item.prompt);
  set(".approval-scope", "Scope: " + item.scopeSummary + " · " + stateLabel(item.behavior));
  set(".approval-proposal", item.proposalText); row.querySelector(".approval-proposal").hidden=!item.proposalText;
  set(".approval-result", stateLabel(item.state) + (["approved","denied","revoked","consumed"].includes(item.state) ? " · Decision recorded in the approval log." : ""));
  for(const [action, permitted] of [["allow",item.canAllow],["deny",item.canDeny],["revoke",item.canRevoke]]) row.querySelector('[data-approval-action="'+action+'"]').hidden=!permitted;
  return row;
 }
 return {messages,preserve,approval};
})();
