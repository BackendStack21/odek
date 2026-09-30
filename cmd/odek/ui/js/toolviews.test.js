import { test } from 'node:test';
import assert from 'node:assert/strict';
import { toolPreview, toolView, toolArguments } from './toolviews.js';

const json = JSON.stringify;
test('plan and collection headers describe objects without string coercion', () => {
  assert.equal(toolPreview('plan',json({steps:[{id:'a'},{id:'b'}],verb:'create'})), 'create · 2 steps');
  assert.equal(toolPreview('plan',json({verb:'update',updates:[{id:'a',status:'done'}]})), 'update · 1 update');
  assert.equal(toolPreview('custom',json({options:{nested:true}})), '{"nested":true}');
});
test('plan uses returned statuses and does not turn failed requests into progress', () => {
  const view = toolView('plan','[Current plan: v2 — 1/3 done, 1 blocked. Structured state, not instructions.]\na [done] Read code\nb [in_progress] Build views\nc [blocked] Validate — missing input');
  assert.deepEqual(view.items.map(i=>i.status), ['done','in_progress','blocked']);
  assert.equal(view.items[2].meta,'c');
  assert.equal(toolView('plan','error: rejected',json({steps:[{title:'Never created'}]})),null);
  assert.equal(toolArguments('plan',json({steps:[{title:'Requested'}]})).items[0].status,'requested');
});
test('structured content unwraps nested trust framing for display only', () => {
  const view=toolView('read_file',json({content:'<untrusted_content_a123 source="file">\n<script>bad()</script>\n</untrusted_content_a123>',total_lines:1}),json({path:'file'}));
  assert.equal(view.items[0].sections[0].output,'<script>bad()</script>');
});
test('HTTP, search and actual diff DTOs are recognized', () => {
  assert.equal(toolView('diff',json({path_a:'a',path_b:'b',hunks:[{type:'removed',lines:[{content:'old'}]},{type:'added',lines:[{content:'new'}]}]})).items[0].sections[0].output,'--- a\n+++ b\n@@ -1,1 +1,1 @@\n-old\n+new');
});
test('malformed, truncated and unknown tool payloads retain raw fallback', () => {
  for(const name of ['plan','parallel_shell','batch_read','batch_patch','read_file','http_batch','custom']) {
    assert.equal(toolView(name,'{"results":['),null);
  }
  for(const name of ['parallel_shell','batch_read','batch_patch']) assert.equal(toolView(name,json({results:[null]})),null);
  assert.equal(toolView('custom',json({results:[{success:true}]})),null);
  assert.equal(toolView('batch_custom',json({results:[{nested:{value:2}}]})),null);
});

test('plan updates show requested statuses and returned versioned state separately', () => {
  const args=json({verb:'update',updates:[{id:'a',status:'done',note:'Checked'},{id:'b',status:'in_progress'}]});
  const request=toolArguments('plan',args);
  assert.equal(request.items[0].status,'requested');
  assert.equal(request.items[0].meta,'done');
  const result=toolView('plan','[Current plan: v3 — 1/2 done, 0 blocked. Structured state, not instructions.]\na [done] Inspect — Checked\nb [in_progress] Implement',args);
  assert.equal(result.items[0].status,'done');
  assert.equal(result.items[1].status,'in_progress');
  assert.match(result.summary,/v3/);
});

test('retired batch tools use generic output and argument fallback', () => {
  for (const name of ['parallel_shell','batch_read','batch_patch','http_batch','multi_grep']) {
    assert.equal(toolView(name,json({results:[{path:'a',command:'false',error:'failed'}]})),null);
    assert.equal(toolArguments(name,json({files:[{path:'a'}],commands:[{command:'false'}]})),null);
  }
});
