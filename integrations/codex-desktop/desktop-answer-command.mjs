import fs from 'node:fs';
import path from 'node:path';
import crypto from 'node:crypto';
import {fileURLToPath,pathToFileURL} from 'node:url';
import {acquireQuestion,parseAnswer,validatePending,answerEnvelope,walkObjects,recordPath} from './desktop-answer-store.mjs';
import {DesktopAnswerClient} from './desktop-answer-ipc.mjs';
import {handleNotificationReply} from './desktop-notifications.mjs';

function readCardRecord(settings,code){
  try{
    return JSON.parse(fs.readFileSync(recordPath(settings.answerDir,code),'utf8'));
  }catch(error){
    if(error?.code==='ENOENT')throw new Error('找不到该问题编号，请从桌面重新发起问题。');
    if(error instanceof SyntaxError)throw new Error('问题记录无效，请从桌面重新发起问题。');
    throw new Error('无法读取问题记录，请检查本地问题目录。');
  }
}

export async function handleAnswer(event,settings,dependencies={}){
  const parts=settings.sessionKey?.split(':');
  if(event.event!=='desktop.answer'||event.project!==settings.project||event.platform!=='feishu'||event.session_key!==settings.sessionKey||event.user_id!==parts?.[2])return {ok:false,message:'仅允许已绑定的飞书私聊回答桌面问题。'};
  if(event.extra?.source==='notification_reply')return handleNotificationReply(event,settings,dependencies);
  let locked,client,submitted=false,code,knownRecord;
  const log=entry=>dependencies.log?.(entry);
  try{
    let parsed;
    if(event.extra?.source==='card'){
      code=String(event.extra.code||'').toUpperCase();
      knownRecord=readCardRecord(settings,code);
      const token=String(event.extra.token||'');
      if(!knownRecord.cardToken||token.length!==knownRecord.cardToken.length||!crypto.timingSafeEqual(Buffer.from(token),Buffer.from(knownRecord.cardToken))||knownRecord.cardMessageId!==event.extra.message_id){knownRecord=null;throw new Error('卡片与原问题不匹配，请在桌面查看。')}
      const custom=typeof event.extra.text==='string'?event.extra.text.trim():'';
      if(Array.from(custom).length>1000)throw new Error('卡片输入最多 1000 字符；较长答案请使用 /answer。');
      const selection=String(event.extra.option??'');
      let answer=custom;
      if(!answer&&/^\d{1,2}$/.test(selection)){
        const option=knownRecord.options?.[Number(selection)];answer=typeof option==='string'?option:option?.label;
      }
      if(!answer)throw new Error('请先选择或填写答案，再提交。');
      if(Array.from(answer).length>8000)throw new Error('答案最多 8000 字符。');
      parsed={code,answer};
    }else{
      parsed=parseAnswer(event.content);code=parsed.code;
      try{knownRecord=JSON.parse(fs.readFileSync(recordPath(settings.answerDir,code),'utf8'))}catch{}
    }
    locked=acquireQuestion(settings.answerDir,code);
    const r=locked.record;
    client=dependencies.client||new DesktopAnswerClient(r.sessionId);
    const state=await client.connect();
    validatePending(state,r);
    const envelope=answerEnvelope(r,parsed.answer);
    validatePending(client.state||state,r);
    r.status='submitting';r.answerHash=crypto.createHash('sha256').update(parsed.answer).digest('hex');r.messageId=event.extra?.message_id;r.attemptAt=Date.now();locked.save();
    // Record before sending: an interrupted process must not silently replay an
    // answer whose outcome is unknown. The desktop remains authoritative.
    submitted=true;
    const accepted=await client.steer(envelope);
    const acceptedTurn=accepted?.result?.result?.turnId;
    if(acceptedTurn!==r.turnId)throw new Error('返回轮次与原问题不一致。');
    r.status='submitted';r.submittedAt=Date.now();locked.save();
    const appeared=await client.waitFor(current=>{let found=false;walkObjects(current,item=>{if(!['userMessage','steeringUserMessage'].includes(item.type))return;const content=item.content||item.input;if(Array.isArray(content)&&content.some(p=>p.type==='text'&&p.text===envelope))found=true});return found});
    r.confirmedInDesktop=appeared;locked.save();
    log({code,status:'submitted',session:r.sessionId,confirmed:appeared});
    return {ok:true,status:'submitted',code,question:r.title.slice(0,1800),answer:parsed.answer.slice(0,1800),cardMessageId:r.cardMessageId,message:'✅ 答案已提交到原 Codex 桌面会话。'+(appeared?'':'请在桌面确认处理结果。')};
  }catch(error){
    if(locked&&submitted&&locked.record.status!=='submitted'){locked.record.status='uncertain';locked.record.lastError=String(error.message).slice(0,200);locked.save()}
    log({code,status:submitted?'uncertain':'rejected',reason:String(error.message).slice(0,200)});
    const status=submitted?'uncertain':(error.questionStatus||'keep');
    return {ok:false,status,code,question:knownRecord?.title?.slice(0,1800),cardMessageId:knownRecord?.cardMessageId,message:submitted?'提交结果尚未确认，请先在桌面查看该问题；为防重复，本编号不会自动重发。':'未提交：'+error.message};
  }finally{client?.close();locked?.release()}
}
async function main(){
  const directory=path.dirname(fileURLToPath(import.meta.url));
  const settings=JSON.parse(fs.readFileSync(path.join(directory,'settings.json'),'utf8'));
  const log=entry=>{const file=path.join(directory,'answer.log');try{if(fs.existsSync(file)&&fs.statSync(file).size>2*1024*1024)fs.renameSync(file,file+'.previous');fs.appendFileSync(file,JSON.stringify({at:new Date().toISOString(),...entry})+'\n')}catch{}};
  const event=JSON.parse(fs.readFileSync(0,'utf8'));
  const result=await handleAnswer(event,settings,{log});
  process.stdout.write(JSON.stringify(result)+'\n');
}
if(process.argv[1]&&import.meta.url===pathToFileURL(path.resolve(process.argv[1])).href){main().catch(()=>{process.stdout.write(JSON.stringify({ok:false,status:'keep',message:'回复桥接未确认成功，请在桌面查看原问题。'})+'\n');process.exitCode=1})}
