import { S, getSessionToken } from './state.js';
import { apiFetch } from './api.js';
import { apiHeaders } from './net.js';
import { showToast } from './utils.js';
let urls = [];
let requestVersion = 0;
const byId = id => document.getElementById(id);
function node(tag, cls, text) { const el=document.createElement(tag);if(cls)el.className=cls;if(text!=null)el.textContent=text;return el; }
function reset() { requestVersion++; for(const url of urls) URL.revokeObjectURL(url);urls=[];const root=byId('artifact-list');if(root)root.textContent=''; }
async function blobFor(item) {
 const response=await fetch('/api/artifacts/'+encodeURIComponent(item.id)+'?session_id='+encodeURIComponent(item.session_id),{headers:apiHeaders({'X-Session-Token':getSessionToken(item.session_id)})});
 if(!response.ok)throw new Error('Artifact unavailable. The preview cache may have expired.');
 return response.blob();
}
function add(item) {
 if(!item || item.session_id!==S.sessionId)return;
 const root=byId('artifact-list');if(!root)return;
 const card=node('article','artifact-card');card.append(node('strong','',item.name),node('p','management-note',item.media_type+' · '+Math.ceil(item.size_bytes/1024)+' KB'));
 const actions=node('div','artifact-actions');
 const download=node('button','management-action','Download');download.type='button';download.addEventListener('click',async()=>{try{const blob=await blobFor(item);const url=URL.createObjectURL(blob);const a=node('a');a.href=url;a.download=item.name;a.click();setTimeout(()=>URL.revokeObjectURL(url),1000);}catch(e){showToast(e.message);}});actions.appendChild(download);
 const detected=String(item.media_type || '').split(';')[0];
 const mime=detected==='application/ogg'?'audio/ogg':detected;
 if (/^(image\/(png|jpeg|gif|webp)|audio\/(mpeg|wav|wave|x-wav|ogg)|application\/pdf)$/.test(mime)) {
  const preview=node('button','management-action','Preview');preview.type='button';
  preview.addEventListener('click',async()=>{preview.disabled=true;const version=requestVersion;try{const blob=await blobFor(item);if(version!==requestVersion)return;const url=URL.createObjectURL(blob);urls.push(url);let media;
   if(mime.startsWith('image/')){media=node('img','artifact-preview');media.alt=item.name;}
   else if(mime.startsWith('audio/')){media=node('audio','artifact-preview');media.controls=true;}
   else {media=node('iframe','artifact-preview artifact-pdf');media.title=item.name;media.setAttribute('sandbox','');}
   media.src=url;card.appendChild(media);preview.textContent='Preview loaded';
  }catch(e){showToast(e.message);preview.disabled=false;}});actions.appendChild(preview);
 }
 card.appendChild(actions);root.appendChild(card);
}
export async function loadArtifacts() {
 reset();const version=requestVersion;const sid=S.sessionId;if(!sid)return;
 try{const data=await apiFetch('/api/artifacts?session_id='+encodeURIComponent(sid),{sessionToken:getSessionToken(sid)});if(version!==requestVersion||sid!==S.sessionId)return;for(const item of data.artifacts||[])add(item);if(!(data.artifacts||[]).length)byId('artifact-list')?.appendChild(node('p','management-note','No captured artifacts in this session.'));if(data.retention)byId('artifact-list')?.appendChild(node('p','management-note',data.retention));}catch(e){if(version===requestVersion){const root=byId('artifact-list');if(root)root.textContent=e.status===404?'Artifact previews are unavailable on this server.':e.message;}}
}
S.onArtifact=add;S.resetArtifacts=reset;
byId('ptab-outputs')?.addEventListener('click',loadArtifacts);
