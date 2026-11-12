import net from 'node:net';
import crypto from 'node:crypto';
import {EventEmitter} from 'node:events';

const LIMIT=64*1024*1024;
export function applyPatches(state, patches) {
  for(const patch of patches){
    if(!Array.isArray(patch.path)||!['add','remove','replace'].includes(patch.op)||patch.path.some(k=>['__proto__','constructor','prototype'].includes(String(k))))throw new Error('Unsupported desktop state patch');
    if(!patch.path.length){if(patch.op==='remove')throw new Error('Invalid root removal');state=patch.value;continue;}
    let parent=state;for(const key of patch.path.slice(0,-1)){if(parent==null)throw new Error('Invalid patch path');parent=parent[key];}
    const key=patch.path.at(-1);
    if(patch.op==='remove'){if(Array.isArray(parent))parent.splice(Number(key),1);else delete parent[key];}
    else if(patch.op==='add'&&Array.isArray(parent)&&key!=='length')parent.splice(Number(key),0,patch.value);
    else parent[key]=patch.value;
  }
  return state;
}
export class DesktopAnswerClient extends EventEmitter {
  pending=new Map();buffer=Buffer.alloc(0);state=null;revision=null;closed=false;
  constructor(sessionId){super();if(!/^[a-zA-Z0-9_-]{8,100}$/.test(sessionId))throw new Error('Invalid session');this.sessionId=sessionId;}
  send(message){if(this.closed||!this.socket?.writable)throw new Error('桌面连接已断开。');const body=Buffer.from(JSON.stringify(message));if(body.length>LIMIT)throw new Error('IPC frame too large');const size=Buffer.alloc(4);size.writeUInt32LE(body.length);this.socket.write(Buffer.concat([size,body]));}
  fail(error){this.failure=error;for(const p of this.pending.values()){clearTimeout(p.timer);p.reject(error)}this.pending.clear();this.emit('failure',error);this.socket?.destroy();}
  async connect(){
    this.socket=net.connect('\\\\.\\pipe\\codex-ipc');
    this.socket.on('data',chunk=>{try{
      this.buffer=Buffer.concat([this.buffer,chunk]);
      if(this.buffer.length>LIMIT+4)throw new Error('IPC buffer too large');
      while(this.buffer.length>=4){const size=this.buffer.readUInt32LE(0);if(size>LIMIT)throw new Error('Invalid IPC frame');if(this.buffer.length<size+4)break;const message=JSON.parse(this.buffer.subarray(4,size+4).toString());this.buffer=this.buffer.subarray(size+4);this.receive(message);}
    }catch(e){this.fail(e)}});
    this.socket.on('error',e=>this.fail(e));
    this.socket.on('close',()=>{if(!this.closed)this.fail(new Error('桌面连接已断开。'))});
    await new Promise((resolve,reject)=>{const timeout=setTimeout(()=>{this.socket.destroy();reject(new Error('桌面连接超时。'))},3000);this.socket.once('connect',()=>{clearTimeout(timeout);resolve()});this.socket.once('error',e=>{clearTimeout(timeout);reject(e)})});
    const init=await this.request('initialize',{clientType:'cli'},0);this.clientId=init.result.clientId;
    const owner=await this.request('thread-owner-discovery',{hostId:'local',conversationId:this.sessionId});this.ownerId=owner.handledByClientId;
    if(!this.ownerId)throw new Error('原桌面会话尚未打开。');
    await this.snapshot();return this.state;
  }
  request(method,params,version=1){
    return new Promise((resolve,reject)=>{const requestId=crypto.randomUUID();const timer=setTimeout(()=>{this.pending.delete(requestId);reject(new Error('桌面接口响应超时。'))},8000);this.pending.set(requestId,{resolve,reject,timer});
      try{this.send({type:'request',requestId,sourceClientId:this.clientId||'initializing-client',version,method,params,timeoutMs:7000,...(this.ownerId?{targetClientId:this.ownerId}:{})})}catch(e){clearTimeout(timer);this.pending.delete(requestId);reject(e)}
    });
  }
  follow(following){this.send({type:'broadcast',method:'thread-stream-following-changed',sourceClientId:this.clientId,targetClientIds:[this.ownerId],version:1,params:{hostId:'local',conversationId:this.sessionId,following}})}
  snapshot(){return new Promise((resolve,reject)=>{const clear=()=>{clearTimeout(timer);this.off('state',ready);this.off('failure',failed)};const ready=()=>{clear();resolve(this.state)};const failed=e=>{clear();reject(e)};const timer=setTimeout(()=>failed(new Error('无法取得原桌面会话状态。')),5000);this.once('state',ready);this.once('failure',failed);this.follow(true)});}
  receive(message){
    if(message.type==='response'){const p=this.pending.get(message.requestId);if(!p)return;this.pending.delete(message.requestId);clearTimeout(p.timer);message.resultType==='success'?p.resolve(message):p.reject(new Error('桌面接口拒绝请求：'+message.error));return;}
    if(message.type==='client-discovery-request'){this.send({type:'client-discovery-response',requestId:message.requestId,response:{canHandle:false}});return;}
    if(message.type!=='broadcast'||message.sourceClientId!==this.ownerId||message.method!=='thread-stream-state-changed'||message.version!==11||message.params?.hostId!=='local'||message.params?.conversationId!==this.sessionId)return;
    const change=message.params.change;
    if(change.type==='snapshot'){this.state=change.conversationState;this.revision=change.revision;}
    else if(change.type==='patches'&&this.state){if(change.baseRevision!==this.revision)throw new Error('桌面状态版本已变化，请稍后重试。');this.state=applyPatches(this.state,change.patches);this.revision=change.revision;}
    else return;
    this.emit('state',this.state);
  }
  async steer(text){
    if(this.failure)throw this.failure;
    const id=crypto.randomUUID();const input=[{type:'text',text,text_elements:[]}];
    const restoreMessage={id,cwd:this.state.cwd,context:{workspaceRoots:[this.state.cwd],collaborationMode:this.state.latestCollaborationMode},text,content:input};
    return this.request('thread-follower-steer-turn',{conversationId:this.sessionId,input,restoreMessage,clientUserMessageId:id});
  }
  async startTurn(text,id){
    if(this.failure)throw this.failure;
    const input=[{type:'text',text,text_elements:[]}];
    const restoreMessage={id,cwd:this.state.cwd,text,content:input};
    return this.request('thread-follower-start-turn',{conversationId:this.sessionId,turnStart:{request:{threadId:this.sessionId,input,clientUserMessageId:id},context:{inheritThreadSettings:true,restoreMessage}}},2);
  }
  async waitFor(predicate,timeout=4000){if(predicate(this.state))return true;return new Promise(resolve=>{const done=value=>{clearTimeout(timer);this.off('state',check);this.off('failure',failed);resolve(value)};const check=state=>{if(predicate(state))done(true)};const failed=()=>done(false);const timer=setTimeout(()=>done(false),timeout);this.on('state',check);this.once('failure',failed)});}
  close(){if(this.closed)return;try{if(this.ownerId&&this.socket?.writable)this.follow(false)}catch{}this.closed=true;for(const p of this.pending.values()){clearTimeout(p.timer);p.reject(new Error('Closed'))}this.pending.clear();this.socket?.destroy();}
}
