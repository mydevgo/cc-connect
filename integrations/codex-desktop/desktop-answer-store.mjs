import fs from 'node:fs';
import path from 'node:path';
import crypto from 'node:crypto';

function questionError(status,message){const error=new Error(message);error.questionStatus=status;return error;}

export function writeJson(file, value) {
  fs.mkdirSync(path.dirname(file), {recursive:true});
  const tmp=file+'.'+process.pid+'.'+crypto.randomBytes(4).toString('hex')+'.tmp';
  try {fs.writeFileSync(tmp,JSON.stringify(value,null,2));fs.renameSync(tmp,file);}
  finally {try{fs.unlinkSync(tmp)}catch{}}
}
export function recordPath(root, code) {
  if(!/^Q-[a-f0-9]{12}$/i.test(code))throw new Error('问题编号格式不正确。');
  return path.join(root,code.toUpperCase()+'.json');
}
export function registerQuestions(event, options, now=Date.now()) {
  if(!options.answerDir || !/(?:^|[._])request_user_input_async$/.test(event.tool_name||'') || !event.tool_use_id || !event.turn_id)return [];
  let input=event.tool_input;
  if(typeof input==='string')try{input=JSON.parse(input)}catch{return []}
  if(!Array.isArray(input?.questions))return [];
  return input.questions.map((q,index)=>{
    if(q.isSecret)return null;
    const title=q.title||q.question;
    if(typeof title!=='string'||!title.trim())return null;
    const itemId=JSON.stringify(['request_user_input_async',event.tool_use_id,index]);
    const code='Q-'+crypto.createHash('sha256').update(event.session_id+'|'+itemId).digest('hex').slice(0,12).toUpperCase();
    const file=recordPath(options.answerDir,code);
    if(!fs.existsSync(file))writeJson(file,{version:1,code,sessionId:event.session_id,turnId:event.turn_id,toolUseId:event.tool_use_id,index,questionItemId:itemId,title,options:q.options||[],cardToken:crypto.randomUUID(),createdAt:now,expiresAt:now+24*60*60*1000,status:'pending'});
    const record=JSON.parse(fs.readFileSync(file,'utf8'));
    return {code,title,options:q.options||[],token:record.cardToken};
  }).filter(Boolean);
}
export function parseAnswer(content) {
  if(typeof content!=='string')throw new Error('用法：/answer 问题编号 答案');
  const match=content.match(/^\s*\/answer\s+(Q-[a-f0-9]{12})\s+([\s\S]*\S)\s*$/i);
  if(!match)throw new Error('用法：/answer Q-问题编号 答案（编号见问题通知）');
  if(Array.from(match[2]).length>8000)throw new Error('答案过长，请控制在 8000 字符以内。');
  return {code:match[1].toUpperCase(),answer:match[2]};
}
export function acquireQuestion(root, code, now=Date.now()) {
  const file=recordPath(root,code);
  if(!fs.existsSync(file))throw new Error('找不到该问题编号，请检查问题通知。');
  const lock=file+'.lock';
  try{fs.mkdirSync(lock)}catch(error){
    if(error.code==='EEXIST')throw questionError('keep','该问题正在处理，或上次处理被中断。请先在桌面确认结果。');
    throw error;
  }
  try {
    const record=JSON.parse(fs.readFileSync(file,'utf8'));
    if(record.code!==code.toUpperCase()||record.version!==1)throw new Error('问题记录不兼容，请在桌面回答。');
    if(record.status==='submitted'||record.status==='resolved')throw questionError(record.status,'该问题已经回答，本次未重复提交。');
    if(record.status==='submitting'||record.status==='uncertain')throw questionError('uncertain','上次提交结果尚未确认，请先在桌面查看，避免重复回答。');
    if(record.status!=='pending'||now>=record.expiresAt)throw questionError('expired','问题已过期，请在桌面查看当前问题。');
    return {record,save:()=>writeJson(file,record),release:()=>{try{fs.rmdirSync(lock)}catch{}}};
  }catch(error){fs.rmdirSync(lock);throw error;}
}

export function walkObjects(root, visit) {
  const seen=new Set();
  const walk=(value)=>{if(!value||typeof value!=='object'||seen.has(value))return;seen.add(value);visit(value);for(const child of Object.values(value))if(child&&typeof child==='object')walk(child)};
  walk(root);
}
function repliedIds(state) {
  const ids=new Set();
  walkObjects(state,item=>{
    if(!['userMessage','steeringUserMessage'].includes(item.type))return;
    if(item.type==='steeringUserMessage'&&item.status!=='accepted'&&item.steerAcknowledged!==true)return;
    const input=item.content||item.input;
    if(!Array.isArray(input))return;
    for(const part of input){
      if(part.type!=='text'||typeof part.text!=='string')continue;
      const start=part.text.indexOf('<send_user_message_question_reply>');
      const end=part.text.lastIndexOf('</send_user_message_question_reply>');
      if(start<0||end<start)continue;
      try{let rows=JSON.parse(part.text.slice(start+'<send_user_message_question_reply>'.length,end).trim());if(!Array.isArray(rows))rows=[rows];for(const row of rows)if(row.questionItemId)ids.add(row.questionItemId)}catch{}
    }
  });
  return ids;
}
export function validatePending(state, record) {
  if(state?.id!==record.sessionId || typeof state.source!=='string' || state.source==='cli' || !/desktop/i.test(state.originator||''))throw new Error('目标不是原来的本机桌面会话。');
  if(repliedIds(state).has(record.questionItemId))throw questionError('resolved','该问题已在桌面或其他入口回答，本次未提交。');
  let question=null,turn=null;
  walkObjects(state,item=>{
    if(item.type==='agentMessage'&&item.id===record.toolUseId&&Array.isArray(item.questions))question=item.questions[record.index];
    if(item.turnId===record.turnId&&Array.isArray(item.items)&&item.status==='inProgress')turn=item;
  });
  if(!question)throw questionError('expired','原问题不在当前桌面状态中，请在桌面查看。');
  if((question.title||question.question)!==record.title)throw questionError('expired','问题内容已改变，请在桌面查看。');
  if(!turn)throw questionError('expired','原问题所在轮次已结束或中断，本次未提交。');
  return {question,turn};
}
export function answerEnvelope(record,answer) {
  return '<send_user_message_question_reply>\n'+JSON.stringify([{questionItemId:record.questionItemId,question:record.title,answer}])+'\n</send_user_message_question_reply>';
}
