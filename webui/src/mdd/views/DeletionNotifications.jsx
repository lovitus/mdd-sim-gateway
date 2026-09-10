import React, {useEffect,useRef,useState} from 'react'
import {createPortal} from 'react-dom'
import {api} from '../api.js'

const cardStates={deleted:'已删除',deleted_observed:'已观察到不存在',not_deleted_observed:'仍存在，未确认删除',pending:'待确认',unknown:'结果未知'}
const deliveryStates={deletion_unconfirmed:'删除尚未确认',pending_notification:'待保存通知',pending_delivery:'已保存，待送达或未知',receiver_acknowledged:'已获接收确认'}
const date=value=>value?new Date(value).toLocaleString():'未记录'

export default function DeletionNotifications({eid,refreshKey=0}) {
  const [archives,setArchives]=useState([]),[operations,setOperations]=useState([])
  const [error,setError]=useState(''),[busy,setBusy]=useState(false)
  const [selected,setSelected]=useState(null),[codes,setCodes]=useState(null)
  const lock=useRef(false),epoch=useRef(0),dialog=useRef(null),opener=useRef(null)
  const close=()=>{epoch.current++;setSelected(null);setCodes(null);opener.current?.focus()}
  const read=async()=>{
    if(lock.current)return
    lock.current=true;setBusy(true);setError('')
    try{const [result,records]=await Promise.all([api.euiccNotificationArchives(eid),api.euiccDeletions(eid)]);setArchives(result.archives||[]);setOperations(records.deletions||[])}catch(e){setError(e.message)}finally{lock.current=false;setBusy(false)}
  }
  useEffect(()=>{void read();return()=>{epoch.current++}},[eid,refreshKey])
  useEffect(()=>{if(selected)dialog.current?.querySelector('button')?.focus()},[selected?.key])
  const recover=async operation=>{
    if(lock.current)return
    lock.current=true;setBusy(true);setError('');let failure=''
    try{await api.recoverEuiccDeletion(eid,operation.operation_id)}catch(e){failure=e.message}finally{lock.current=false;setBusy(false)}
    await read();if(failure)setError(failure)
  }
  const replay=async archive=>{
    if(lock.current)return
    if(!window.confirm(`${archive.attempts?.length?'重发':'发送'} ${selected?.name||''}（ICCID ${archive.entry.iccid}）的删除通知至 ${archive.entry.address}？运营商可能据此永久停用服务。`))return
    if(!window.confirm('确认这是一次新的发送尝试？成功、失败或未知结果均保留原始通知，不会自动重试。'))return
    if(window.prompt('输入完整 ICCID，确认本次发送：')!==archive.entry.iccid)return
    lock.current=true;setBusy(true);setError('')
    try{
      const result=await api.replayEuiccNotificationArchive(eid,archive.entry.sequence_number,{operation_id:`replay-${crypto.randomUUID()}`,archive_sha256:archive.sha256,confirm_iccid:archive.entry.iccid,confirm_operator_deactivation:true,confirm_retain_notification:true})
      setError(result.state==='acknowledged'?'已获 HTTP 接收确认；不代表重新下载资格已释放，通知仍保留。':`本次结果：${result.state||'unknown'}；不会自动重发。`)
      const [refreshed,records]=await Promise.all([api.euiccNotificationArchives(eid),api.euiccDeletions(eid)]);setArchives(refreshed.archives||[]);setOperations(records.deletions||[])
    }catch(e){setError(`发送结果未确认：${e.message}。请先重新读取记录，不要重复点击。`)}finally{lock.current=false;setBusy(false)}
  }
  const linked=new Set(operations.flatMap(({operation})=>operation.notifications||[]))
  const rows=operations.map(({operation,delivery,recovery})=>({key:operation.operation_id,operation,delivery,recovery,iccid:operation.iccid,name:operation.profile_name||recovery?.profile_name||'未记录名称',provider:operation.service_provider_name||recovery?.service_provider_name||'未记录服务商',archives:archives.filter(a=>(operation.notifications||[]).includes(a.entry.sequence_number))}))
  for(const a of archives)if(!linked.has(a.entry.sequence_number))rows.push({key:`archive-${a.entry.sequence_number}-${a.sha256}`,iccid:a.entry.iccid,name:'未记录名称',provider:'未记录服务商',archives:[a]})
  const detail=selected&&rows.find(row=>row.key===selected.key)
  const reveal=async()=>{
    if(!detail||lock.current)return
    if(!window.confirm('显示敏感的下载恢复码？请勿将详情截图或代码转发给他人。'))return
    const current=epoch.current;lock.current=true;setBusy(true)
    try{const result=await api.euiccRecoveryCodes(eid,detail.iccid,{confirmed:true,confirm_iccid:detail.iccid,download_operation_id:detail.recovery?.download_operation_id});if(epoch.current===current)setCodes(result)}catch(e){if(epoch.current===current)setError(e.message)}finally{lock.current=false;setBusy(false)}
  }
  const field=(label,value)=><div><dt>{label}</dt><dd>{value||'未记录'}</dd></div>
  return <>
    <div className="u-deletion-list">
      <div className="u-deletion-toolbar"><b>删除记录与通知</b><button className="btn btn-ghost" disabled={busy} onClick={read}>刷新记录</button></div>
      {error&&!detail&&<p role="status" className="u-error">{error}</p>}
      {rows.map(row=><div className="u-deletion-row" key={row.key}>
        <div><strong>{row.name}</strong><span> · {row.provider}</span><div className="u-hint">ICCID {row.iccid} · {deliveryStates[row.delivery]||'已保存通知'}</div></div>
        <button className="btn btn-ghost" onClick={e=>{epoch.current++;opener.current=e.currentTarget;setCodes(null);setError('');setSelected({key:row.key,name:row.name})}}>打开详情</button>
      </div>)}
      {!busy&&rows.length===0&&<p className="u-hint">暂无删除记录或已保存的删除通知。</p>}
    </div>
    {detail&&createPortal(<div className="u-modal-backdrop" onClick={close}>
      <div ref={dialog} className="u-deletion-dialog" role="dialog" aria-modal="true" aria-labelledby="deletion-detail-title" onClick={e=>e.stopPropagation()} onKeyDown={e=>{
        if(e.key==='Escape'){e.stopPropagation();close()}
        if(e.key==='Tab'){const items=Array.from(dialog.current.querySelectorAll('button:not([disabled]),textarea'));const first=items[0],last=items.at(-1);if(e.shiftKey&&document.activeElement===first){e.preventDefault();last?.focus()}else if(!e.shiftKey&&document.activeElement===last){e.preventDefault();first?.focus()}}
      }}>
        <header><h3 id="deletion-detail-title">删除通知详情</h3><button className="u-modal-close" aria-label="关闭详情" onClick={close}>×</button></header>
        <dl className="u-deletion-fields">
          {field('原 profile 名称',detail.name)}{field('服务商',detail.provider)}{field('删除前昵称',detail.operation?.profile_nickname)}
          {field('EID',eid)}{field('ICCID',detail.iccid)}{field('记录时间',date(detail.operation?.created_at))}
          {field('卡内状态',cardStates[detail.operation?.state])}{field('通知状态',deliveryStates[detail.delivery])}
          {field('名称资料来源',detail.operation?.metadata_source==='download_receipt'?'原下载回执':detail.operation?.metadata_source==='card_before_delete'?'删除前卡片回读':'未记录')}
        </dl>
        {detail.operation&&<button className="btn btn-ghost" disabled={busy} onClick={()=>recover(detail.operation)}>恢复读取状态和通知</button>}
        <h4>下载恢复资料</h4>
        {detail.recovery&&<p className="u-hint">来源：{detail.recovery.code_source==='operator_recovered'?'从原有资料补录，未验证代码仍可用':detail.recovery.code_source==='download_request'?'原下载请求':'仅有下载回执，未保存代码'}</p>}
        <p className="u-hint">激活码：{detail.recovery?.activation_code_saved?'已保存':'未保存'} · 确认码：{detail.recovery?.confirmation_code_saved?'已保存':'未保存'}</p>
        {!!(detail.recovery?.activation_code_saved||detail.recovery?.confirmation_code_saved)&&!codes&&<button className="btn btn-ghost" disabled={busy} onClick={reveal}>显示敏感恢复码</button>}
        {codes&&<><label>激活码<textarea readOnly rows={2} value={codes.activation_code||''}/></label><label>确认码<textarea readOnly rows={1} value={codes.confirmation_code||''}/></label><button className="btn btn-ghost" onClick={()=>setCodes(null)}>隐藏恢复码</button><p className="u-hint">保存不代表这些代码仍可用于重新下载。</p></>}
        {detail.archives.length===0&&<p className="u-hint">尚无本次删除的真实通知存档；不能把整件操作报为完成。</p>}
        {detail.archives.map(a=><section className="u-deletion-archive" key={a.sha256}>
          <h4>删除通知 #{a.entry.sequence_number}</h4><dl className="u-deletion-fields">{field('接收端',a.entry.address)}{field('SHA-256',a.sha256)}</dl>
          <ul>{a.attempts?.map(attempt=><li key={attempt.id}>{date(attempt.at)} · {attempt.state==='acknowledged'?'HTTP 接收确认':attempt.state==='failed'?'发送被拒绝':'结果未知'}</li>)}</ul>
          <button className="btn btn-danger-outline" disabled={busy} onClick={()=>replay(a)}>{a.attempts?.length?'确认后重发删除通知':'确认后发送删除通知'}</button>
        </section>)}
        {error&&<p role="status" className="u-error">{error}</p>}
      </div>
    </div>,document.body)}
  </>
}
