import fs from 'node:fs';
import {spawnSync} from 'node:child_process';
import {recordPath,writeJson} from './desktop-answer-store.mjs';

export function sendQuestionCards(questions,event,settings,context={}){
  for(const question of questions){
    const file=recordPath(settings.answerDir,question.code);
    const record=JSON.parse(fs.readFileSync(file,'utf8'));
    if(record.cardMessageId)continue;
    const options=question.options.map(option=>typeof option==='string'?{label:option}:{label:option.label,description:option.description||''});
    const payload={code:question.code,token:record.cardToken,title:question.title,options,conversation:context.title||'',directory:event.cwd||context.cwd||''};
    const run=(settings.cardSender||((data)=>spawnSync(settings.ccBinary,['desktop-card','--project',settings.project,'--session',settings.sessionKey],{input:JSON.stringify(data),encoding:'utf8',windowsHide:true,timeout:25000,maxBuffer:256*1024})))(payload);
    if(run.status!==0||run.error)throw new Error('问题卡片发送失败：'+(run.error?.code||String(run.stderr||'').slice(0,300)));
    let messageId;try{messageId=JSON.parse(run.stdout).message_id}catch{}
    if(typeof messageId!=='string'||!messageId.startsWith('om_'))throw new Error('问题卡片未返回消息标识。');
    // Merge with latest state so a very fast callback cannot be overwritten.
    const current=JSON.parse(fs.readFileSync(file,'utf8'));
    writeJson(file,{...current,cardMessageId:messageId,cardSentAt:Date.now()});
  }
}
