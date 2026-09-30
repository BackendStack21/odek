import { test } from 'node:test';
import assert from 'node:assert/strict';
import { checkEvidence, summarizeEvidence } from './evidence.js';
const check=(command,output,outcome='completed')=>({name:'shell',args:JSON.stringify({command}),output,outcome});
test('a narrower passing check cannot erase a broad failure',()=>{
 const result=summarizeEvidence([check('go test ./...','FAIL package','failed'),check('go test ./internal/session','ok package 0.1s')]);
 assert.equal(result.checks.length,2);assert.equal(result.checks[0].state,'failed');assert.equal(result.checks[1].state,'passed');
});
test('only exact-command reruns supersede earlier attempts',()=>{
 const result=summarizeEvidence([check('go test ./...','FAIL package','failed'),check('go test ./...','ok package 0.1s')]);
 assert.deepEqual(result.checks[0].previous,['failed']);assert.equal(result.checks[0].state,'passed');
});
test('empty and historical outcomes cannot imply validation',()=>{
 assert.equal(checkEvidence(check('npm test','')).state,'unverified');
 assert.equal(checkEvidence(check('go test ./...','ok package 0.1s','unknown')).state,'unverified');
 assert.equal(checkEvidence(check('node --test','1..1\n# pass 1\n# fail 0')).state,'passed');
 assert.equal(checkEvidence(check('node --test','not ok 1\n# fail 1')).state,'failed');
});
test('failed edits and read diffs are not file changes',()=>{
 const item=(name,outcome)=>({name,outcome,args:'{"path":"src/main.go"}',output:'{"success":false,"error":"rejected"}'});
 assert.equal(summarizeEvidence([item('patch','failed'),item('diff','completed'),item('write_file','completed')]).files.length,0);
});


test('failure summaries dominate earlier passing rows even when shell exits zero',()=>{
 assert.equal(checkEvidence(check('pytest || true','1 failed, 4 passed in 0.2s')).state,'failed');
 assert.equal(checkEvidence(check('node --test || true','ok package 0.1s\n# pass 5\n# fail 1')).state,'failed');
});


test('zero executed tests do not establish passing validation',()=>{
 assert.equal(checkEvidence(check('pytest','0 passed, 5 skipped in 0.2s')).state,'unverified');
});
