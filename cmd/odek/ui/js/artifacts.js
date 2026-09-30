import { S, getSessionToken } from './state.js';
import { apiFetch } from './api.js';
import { apiHeaders } from './net.js';
import { showToast } from './utils.js';
let urls = [];
let requestVersion = 0;
let artifacts=new Map();
const byId = id => document.getElementById(id);
function node(tag, cls, text) { const el=document.createElement(tag);if(cls)el.className=cls;if(text!=null)el.textContent=text;return el; }
function reset() { requestVersion++; for(const url of urls) URL.revokeObjectURL(url);urls=[];artifacts.clear();S.artifactCount=0;const root=byId('artifact-list');if(root)root.textContent=''; }
async function blobFor(item) {
 const response=await fetch('/api/artifacts/'+encodeURIComponent(item.id)+'?session_id='+encodeURIComponent(item.session_id),{headers:apiHeaders({'X-Session-Token':getSessionToken(item.session_id)})});
 if(!response.ok)throw new Error('This artifact is no longer available. Ask the agent to generate it again.');
 return response.blob();
}
function add(item) {
 if(!item || item.session_id!==S.sessionId)return;
 const root=byId('artifact-list');if(!root || artifacts.has(item.id))return;artifacts.set(item.id,item);S.artifactCount=artifacts.size;S.paintEvidence?.();
 const card=node('article','artifact-card');card.append(node('strong','',item.name),node('p','management-note',item.media_type+' · '+Math.ceil(item.size_bytes/1024)+' KB'));
 card.dataset.artifactId=item.id;
 const sameName=[...artifacts.values()].filter(other=>other.name===item.name);
 const revision=node('p','management-note','Capture '+sameName.length+' with this filename · most recent capture');revision.dataset.captureLabel=item.name;
 root.querySelectorAll('[data-capture-label]').forEach(label=>{if(label.dataset.captureLabel===item.name)label.textContent=label.textContent.replace(' · most recent capture','');});card.appendChild(revision);
 const details=node('details','artifact-provenance');details.append(node('summary','','Capture details'),node('p','management-note','Captured '+(item.created_at || '')+' · '+(item.turn_id || 'turn unavailable')+' · SHA-256 '+(item.sha256 || 'unavailable')));card.appendChild(details);
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
 try{const data=await apiFetch('/api/artifacts?session_id='+encodeURIComponent(sid),{sessionToken:getSessionToken(sid)});if(version!==requestVersion||sid!==S.sessionId)return;for(const item of data.artifacts||[])add(item);if(!(data.artifacts||[]).length)byId('artifact-list')?.appendChild(node('p','management-note','No captured artifacts in this session.'));if((data.artifacts||[]).length)byId('artifact-list')?.appendChild(node('p','management-note','Download artifacts you want to keep.'));}catch(e){if(version===requestVersion){const root=byId('artifact-list');if(root)root.textContent=e.status===404?'Artifact previews are unavailable on this server.':e.message;}}
}
S.onArtifact=add;S.resetArtifacts=reset;
S.loadArtifacts = loadArtifacts;
