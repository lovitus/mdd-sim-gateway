import React, {useEffect,useRef,useState} from 'react'
import {api} from '../api.js'

export default function DeletionNotifications({eid,refreshKey=0}) {
  const [archives,setArchives]=useState(null)
  const [operations,setOperations]=useState([])
  const [error,setError]=useState('')
  const [busy,setBusy]=useState(false)
  const lock=useRef(false)
  const read=async()=>{
    if(lock.current)return
    lock.current=true;setBusy(true);setError('')
    try{const [result,records]=await Promise.all([api.euiccNotificationArchives(eid),api.euiccDeletions(eid)]);setArchives(result.archives||[]);setOperations(records.deletions||[])}catch(e){setError(e.message)}finally{lock.current=false;setBusy(false)}
  }
  useEffect(()=>{void read()},[eid,refreshKey])
  const recover=async operation=>{
    if(lock.current)return
    lock.current=true;setBusy(true);setError('')
    let failure=''
    try{await api.recoverEuiccDeletion(eid,operation.operation_id)}catch(e){failure=e.message}finally{lock.current=false;setBusy(false)}
    await read()
    if(failure)setError(failure)
  }
  const replay=async archive=>{
    if(lock.current)return
    if(!window.confirm(`${archive.attempts?.length?'重发':'发送'} ICCID ${archive.entry.iccid} 的删除通知？运营商可能据此永久停用服务。`))return
    if(!window.confirm('确认这是一次新的发送尝试？成功、失败或未知结果均保留原始通知，不会自动重试。'))return
    if(window.prompt('输入完整 ICCID，确认本次重发：')!==archive.entry.iccid)return
    lock.current=true;setBusy(true);setError('')
    try{
      const result=await api.replayEuiccNotificationArchive(eid,archive.entry.sequence_number,{operation_id:`replay-${crypto.randomUUID()}`,archive_sha256:archive.sha256,confirm_iccid:archive.entry.iccid,confirm_operator_deactivation:true,confirm_retain_notification:true})
      setError(result.state==='acknowledged'?'已获 HTTP 接收确认；不代表重新下载资格已释放，通知仍保留。':`本次结果：${result.state||'unknown'}；不会自动重发。`)
      const [refreshed,records]=await Promise.all([api.euiccNotificationArchives(eid),api.euiccDeletions(eid)]);setArchives(refreshed.archives||[]);setOperations(records.deletions||[])
    }catch(e){setError(`发送结果未确认：${e.message}。请先重新读取记录，不要重复点击。`)}finally{lock.current=false;setBusy(false)}
  }
  return <section style={{marginTop:16}}>
    <h3>删除通知存档</h3>
    <button className="btn btn-ghost" disabled={busy} onClick={read}>读取存档记录</button>
    {error&&<p role="status">{error}</p>}
    {operations.map(({operation,delivery})=><div key={operation.operation_id} style={{marginTop:12,overflowWrap:'anywhere'}}>
      <div>ICCID {operation.iccid} · {operation.created_at}</div>
      <div>卡内状态：{{deleted:'已删除',deleted_observed:'已观察到不存在',not_deleted_observed:'仍存在，未确认删除',pending:'待确认',unknown:'结果未知'}[operation.state]||operation.state}</div>
      <div>通知状态：{{deletion_unconfirmed:'删除尚未确认',pending_notification:'待取得并保存真实通知',pending_delivery:'已保存，待送达或结果未知',receiver_acknowledged:'已获 HTTP 接收确认，非重新下载资格确认'}[delivery]||delivery}</div>
      <button className="btn btn-ghost" disabled={busy} onClick={()=>recover(operation)}>恢复读取状态和通知</button>
    </div>)}
    {archives?.length===0&&<p>尚无已保存的真实删除通知。需要原卡恢复读取时，不会把整件删除操作报为完成。</p>}
    {archives?.map(a=><div key={`${a.entry.sequence_number}:${a.sha256}`} style={{marginTop:12,overflowWrap:'anywhere'}}>
      <div>ICCID {a.entry.iccid} · 通知 {a.entry.sequence_number}</div>
      <div>SHA-256 {a.sha256}</div>
      <ul>{a.attempts?.map(attempt=><li key={attempt.id}>{attempt.at} · {attempt.state}</li>)}</ul>
      <button className="btn btn-danger-outline" disabled={busy} onClick={()=>replay(a)}>{a.attempts?.length?'确认后重发删除通知':'确认后发送删除通知'}</button>
    </div>)}
  </section>
}
