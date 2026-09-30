// Execution evidence stays distinct from the agent's claims about task success.
import { classifyToolResult, collectReceipt } from './tools.js';
import { resultText } from './results.js';

export function checkEvidence(item) {
  let args; try { args=JSON.parse(item.args || '{}'); } catch { args={}; }
  const command=args.command || '';
  const output=resultText(item.output || '');
  const chips=classifyToolResult(item.name,output).filter(chip=>chip.kind==='test');
  if(item.name!=='shell' || (!chips.length && !/\b(go test|pytest|npm test|node --test|cargo test|make test)\b/.test(command)))return null;
  const failed=item.outcome==='failed' || chips.some(chip=>chip.tone==='danger') || /^FAIL\b|^--- FAIL:|^not ok\b|^# fail [1-9]\d*\b|\b[1-9]\d* (?:failed|errors?)\b/m.test(output);
  const passed=item.outcome==='completed' && !failed && (chips.some(chip=>chip.tone==='ok') || /^# fail 0$/m.test(output) && /^# pass [1-9]\d*$/m.test(output));
  return {command,state:failed?'failed':passed?'passed':'unverified',item};
}

export function summarizeEvidence(items) {
  const files=new Map(),checks=new Map();let failed=0,unknown=0;
  for(const item of items){
    if(item.outcome==='failed')failed++;
    else if(item.outcome!=='completed')unknown++;
    const check=checkEvidence(item);
    if(check){const previous=checks.get(check.command);check.previous=previous ? [...previous.previous,previous.state] : [];checks.set(check.command,check);}
    if(item.outcome==='completed' && /^(patch|write_file)$/.test(item.name)){
      const text=resultText(item.output || '');
      let data;try{data=JSON.parse(text);}catch{data=null;}
      if(data?.success===false || data?.error)continue;
      for(const path of collectReceipt(item.name,item.args,text).files)files.set(path,item);
    }
  }
  return {files:[...files].map(([path,item])=>({path,item})),checks:[...checks.values()],failed,unknown};
}
