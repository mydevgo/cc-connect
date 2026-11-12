import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import {sendDesktopNotification,validateNotificationReply} from '../desktop-notifications.mjs';
import {handleAnswer} from '../desktop-answer-command.mjs';
import {DesktopAnswerClient} from '../desktop-answer-ipc.mjs';

const dir=fs.mkdtempSync(path.join(os.tmpdir(),'desktop-notification-test-'));
try {
 const settings={project:'desktop',sessionKey:'feishu:oc_chat:ou_owner',answerDir:dir};
 const event={session_id:'desktop-original-session',turn_id:'turn-old',last_assistant_message:'你希望采用哪种口径？'};
 let sends=0;
 settings.noticeSender=notice=>{sends++;assert.match(notice.text,/可引用这条通知/);return {status:0,stdout:JSON.stringify({message_id:'om_notice'})}};
 assert.equal(sendDesktopNotification(event,settings,'[Codex · 回复完成]\n你希望采用哪种口径？'),'om_notice');
 sendDesktopNotification(event,settings,'same');assert.equal(sends,1);
 const noticeFile=fs.readdirSync(path.join(dir,'notifications')).find(n=>n.startsWith('N-'));
 const record=JSON.parse(fs.readFileSync(path.join(dir,'notifications',noticeFile),'utf8'));
 const state={id:event.session_id,hostId:'local',source:'vscode',originator:'Codex Desktop',cwd:'E:/workspace',threadRuntimeStatus:{type:'idle'},latestTurnStartMessageId:'client-old',turns:[{turnId:'turn-old',status:'completed',turnStartedAtMs:1,params:{clientUserMessageId:'client-old'},items:[{type:'agentMessage',text:event.last_assistant_message}]}]};
 const base={event:'desktop.answer',project:settings.project,platform:'feishu',session_key:settings.sessionKey,user_id:'ou_owner',content:'采用长期账本 "$()"\n第二行',extra:{source:'notification_reply',parent_message_id:'om_notice',message_id:'om_reply'}};
 let started=0,clientId;
 const client={state,connect:async()=>state,startTurn:async(text,id)=>{started++;assert.equal(text,base.content);assert.ok(id);clientId=id;return {result:{result:{turn:{id:'turn-new'}}}}},waitFor:async predicate=>predicate({...state,turns:[...state.turns,{turnId:'turn-new',params:{clientUserMessageId:clientId},items:[]}]}),close(){}};
 let receipt=await handleAnswer({...base,user_id:'ou_other'},settings,{client});assert.equal(receipt.ok,false);assert.equal(started,0);
 receipt=await handleAnswer({...base,extra:{...base.extra,parent_message_id:'om_legacy'}},settings,{client});assert.equal(receipt.ok,false);assert.match(receipt.message,/未建立可回复绑定/);assert.equal(started,0);
 assert.throws(()=>validateNotificationReply({...state,source:'cli'},record),/不是原来/);
 assert.throws(()=>validateNotificationReply({...state,id:'different'},record),/不是原来/);
 assert.throws(()=>validateNotificationReply({...state,threadRuntimeStatus:{type:'active'}},record),/正在执行/);
 assert.throws(()=>validateNotificationReply({...state,turns:[...state.turns,{turnId:'newer',status:'completed',turnStartedAtMs:2,items:[]}]},record),/已有更新/);
 assert.throws(()=>validateNotificationReply(state,{...record,expiresAt:0}),/过期/);
 assert.throws(()=>validateNotificationReply(state,{...record,finalText:'different'}),/不一致/);
 receipt=await handleAnswer(base,settings,{client});assert.equal(receipt.ok,true);assert.equal(started,1);
 const saved=JSON.parse(fs.readFileSync(path.join(dir,'notifications',noticeFile),'utf8'));assert.equal(saved.confirmedInDesktop,true);assert.equal(saved.newTurnId,'turn-new');
 receipt=await handleAnswer(base,settings,{client});assert.equal(receipt.ok,false);assert.equal(started,1);assert.match(receipt.message,/已经回复/);
 fs.writeFileSync(path.join(dir,'notifications',noticeFile),JSON.stringify(record));
 receipt=await handleAnswer(base,settings,{client:{...client,startTurn:async()=>{throw new Error('timeout')}}});assert.equal(receipt.status,'uncertain');
 receipt=await handleAnswer(base,settings,{client});assert.equal(receipt.ok,false);assert.equal(started,1);

 const transport=new DesktopAnswerClient('desktop-original-session');transport.state=state;
 transport.request=async(method,params,version)=>{assert.equal(method,'thread-follower-start-turn');assert.equal(version,2);assert.equal(params.conversationId,state.id);assert.equal(params.turnStart.request.threadId,state.id);assert.equal(params.turnStart.context.inheritThreadSettings,true);assert.equal(params.turnStart.request.clientUserMessageId,'client-new');assert.equal(params.turnStart.request.sandboxPolicy,undefined);return {}};
 await transport.startTurn('literal reply','client-new');
 console.log('PASS: durable quote binding, original idle thread continuation, stale/busy/unauthorized/legacy rejection, duplicate/uncertain protection, inherited desktop permissions.');
}finally{fs.rmSync(dir,{recursive:true,force:true})}
