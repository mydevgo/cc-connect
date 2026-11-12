import fs from 'node:fs';
import path from 'node:path';
import crypto from 'node:crypto';
import {spawnSync} from 'node:child_process';
import {writeJson,walkObjects} from './desktop-answer-store.mjs';
import {DesktopAnswerClient} from './desktop-answer-ipc.mjs';

const hash=value=>crypto.createHash('sha256').update(value).digest('hex');
const root=settings=>settings.notificationDir||path.join(settings.answerDir,'notifications');
const recordFile=(settings,code)=>{
  if(!/^N-[A-F0-9]{16}$/.test(code))throw new Error('通知编号无效。');
  return path.join(root(settings),code+'.json');
};
const messageFile=(settings,id)=>{
  if(!/^om_[A-Za-z0-9_-]{1,150}$/.test(id))throw new Error('引用消息标识无效。');
  return path.join(root(settings),'messages',hash(id)+'.json');
};

export function sendDesktopNotification(event,settings,message,now=Date.now()){
  if(!event.turn_id)throw new Error('桌面通知缺少轮次标识。');
  const code='N-'+hash(event.session_id+'|'+event.turn_id+'|complete').slice(0,16).toUpperCase();
  const file=recordFile(settings,code);
  let record;
  if(fs.existsSync(file))record=JSON.parse(fs.readFileSync(file,'utf8'));
  else {
    record={version:1,code,sessionId:event.session_id,turnId:event.turn_id,finalText:String(event.last_assistant_message||'').trim(),createdAt:now,expiresAt:now+24*60*60*1000,status:'pending'};
    writeJson(file,record);
  }
  if(record.messageId){writeJson(messageFile(settings,record.messageId),{code,messageId:record.messageId});return record.messageId}
  const notice={code,text:message+'\n\n通知编号：'+code+'\n可引用这条通知并用文字回复，继续原桌面会话。'};
  const run=(settings.noticeSender||((data)=>spawnSync(settings.ccBinary,['desktop-notice','--project',settings.project,'--session',settings.sessionKey],{input:JSON.stringify(data),encoding:'utf8',windowsHide:true,timeout:25000,maxBuffer:256*1024})))(notice);
  if(run.error||run.status!==0)throw new Error('桌面通知发送失败：'+(run.error?.code||String(run.stderr||'').slice(0,200)));
  let messageId;try{messageId=JSON.parse(run.stdout).message_id}catch{}
  if(typeof messageId!=='string'||!/^om_[A-Za-z0-9_-]{1,150}$/.test(messageId))throw new Error('桌面通知未返回消息标识。');
  record.messageId=messageId;writeJson(file,record);
  writeJson(messageFile(settings,messageId),{code,messageId});
  return messageId;
}

export function validateNotificationReply(state,record,now=Date.now()){
  if(state?.id!==record.sessionId||state.hostId&&state.hostId!=='local'||typeof state.source!=='string'||state.source==='cli'||!/desktop/i.test(state.originator||''))throw new Error('目标不是原来的本机桌面会话。');
  if(record.status!=='pending')throw new Error(record.status==='uncertain'?'上次提交结果未确认，请先检查桌面，避免重复执行。':'这条通知已经回复，请引用新的桌面通知。');
  if(now>=record.expiresAt)throw new Error('这条通知已过期，请到桌面确认当前问题。');
  const turns=[];
  walkObjects(state,item=>{if(item.turnId&&Array.isArray(item.items))turns.push(item)});
  if(state.threadRuntimeStatus?.type==='active'||turns.some(turn=>turn.status==='inProgress'))throw new Error('原桌面会话正在执行任务，请等待新的完成通知再回复。');
  const original=turns.find(turn=>turn.turnId===record.turnId);
  if(!original||original.status!=='completed')throw new Error('原通知对应的轮次已不可用。');
  if(turns.some(turn=>turn!==original&&(turn.turnStartedAtMs??0)>(original.turnStartedAtMs??0))||state.latestTurnStartMessageId&&original.params?.clientUserMessageId!==state.latestTurnStartMessageId)throw new Error('原会话已有更新，请引用最新的完成通知。');
  if(!original.items.some(item=>item.type==='agentMessage'&&typeof item.text==='string'&&item.text.trim()===record.finalText))throw new Error('原桌面回复与通知记录不一致，请在桌面确认。');
  return original;
}

export async function handleNotificationReply(event,settings,dependencies={}){
  let client,file,lock,record,attempted=false;
  const log=entry=>dependencies.log?.(entry);
  try {
    const parent=event.extra?.parent_message_id;
    const indexPath=messageFile(settings,parent);
    if(!fs.existsSync(indexPath))throw new Error('这条通知未建立可回复绑定（可能是旧通知或问题卡片）。请回桌面回答，问题卡片请使用“提交答案”。本次没有启动普通任务。');
    const index=JSON.parse(fs.readFileSync(indexPath,'utf8'));
    file=recordFile(settings,index.code);
    lock=file+'.lock';
    try{fs.mkdirSync(lock)}catch{lock=null;throw new Error('这条通知正在处理，请稍候并先检查桌面。')}
    record=JSON.parse(fs.readFileSync(file,'utf8'));
    if(record.version!==1||record.messageId!==parent||index.messageId!==parent)throw new Error('通知绑定不一致，本次未提交。');
    const text=typeof event.content==='string'?event.content.trim():'';
    if(!text||Array.from(text).length>8000||text.startsWith('/'))throw new Error('请引用通知并填写 1～8000 字的文字回复；命令请单独发送。');
    if(!/^om_[A-Za-z0-9_-]{1,150}$/.test(event.extra?.message_id||''))throw new Error('回复消息标识无效。');
    client=dependencies.client||new DesktopAnswerClient(record.sessionId);
    const state=await client.connect();validateNotificationReply(state,record);
    validateNotificationReply(client.state||state,record);
    record.status='submitting';record.replyMessageId=event.extra.message_id;record.clientUserMessageId=crypto.randomUUID();record.attemptAt=Date.now();writeJson(file,record);
    attempted=true;
    const response=await client.startTurn(text,record.clientUserMessageId);
    const turnId=response?.result?.result?.turn?.id;
    if(typeof turnId!=='string'||!turnId||turnId===record.turnId)throw new Error('桌面未返回新的轮次标识。');
    record.status='submitted';record.newTurnId=turnId;record.submittedAt=Date.now();writeJson(file,record);
    const confirmed=await client.waitFor(current=>{let found=false;walkObjects(current,item=>{if(item.type==='userMessage'&&item.id===record.clientUserMessageId)found=true;if(item.turnId===turnId&&item.params?.clientUserMessageId===record.clientUserMessageId)found=true});return found});
    record.confirmedInDesktop=confirmed;writeJson(file,record);
    log({code:record.code,status:'submitted',source:'notification_reply',session:record.sessionId,confirmed});
    return {ok:true,status:'submitted',message:'[Codex · 引用回复结果]\n✅ 已将回复送到原 Codex 桌面会话并开始下一轮。'+(confirmed?'':'请在桌面确认处理结果。')};
  }catch(error){
    if(attempted&&record){record.status='uncertain';writeJson(file,record)}
    log({code:record?.code,status:attempted?'uncertain':'rejected',source:'notification_reply',reason:String(error.message).slice(0,250)});
    return {ok:false,status:attempted?'uncertain':'keep',message:'[Codex · 引用回复结果]\n'+(attempted?'提交结果未确认，请先检查原桌面会话；为防重复执行，本通知不会自动重发。':'未提交：'+error.message)};
  }finally{client?.close();if(lock)try{fs.rmdirSync(lock)}catch{}}
}
