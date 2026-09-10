import React, {useRef,useState} from 'react'
import {api} from '../api.js'

export default function DeletionNotifications({eid}) {
  const [archives,setArchives]=useState(null)
  const [error,setError]=useState('')
  const [busy,setBusy]=useState(false)
  const lock=useRef(false)
  const read=async()=>{
    if(lock.current)return
    lock.current=true;setBusy(true);setError('')
    try{const result=await api.euiccNotificationArchives(eid);setArchives(result.archives||[])}catch(e){setError(e.message)}finally{lock.current=false;setBusy(false)}
  }
  const replay=async archive=>{
    if(lock.current)return
    if(!window.confirm(`重发 ICCID ${archive.entry.iccid} 的删除通知？运营商可能据此永久停用服务。`))return
    if(!window.confirm('确认这是一次新的发送尝试？成功、失败或未知结果均保留原始通知，不会自动重试。'))return
    if(window.prompt('输入完整 ICCID，确认本次重发：')!==archive.entry.iccid)return
    lock.current=true;setBusy(true);setError('')
    try{
      const result=await api.replayEuiccNotificationArchive(eid,archive.entry.sequence_number,{operation_id:`replay-${crypto.randomUUID()}`,archive_sha256:archive.sha256,confirm_iccid:archive.entry.iccid,confirm_operator_deactivation:true,confirm_retain_notification:true})
      setError(result.state==='acknowledged'?'运营商已确认接收；通知仍保留。':`本次结果：${result.state||'unknown'}；不会自动重发。`)
      const refreshed=await api.euiccNotificationArchives(eid);setArchives(refreshed.archives||[])
    }catch(e){setError(`发送结果未确认：${e.message}。请先重新读取记录，不要重复点击。`)}finally{lock.current=false;setBusy(false)}
  }
  return <section style={{marginTop:16}}>
    <h3>删除通知存档</h3>
    <button className="btn btn-ghost" disabled={busy} onClick={read}>读取存档记录</button>
    {error&&<p role="status">{error}</p>}
    {archives?.length===0&&<p>没有已保存的真实删除通知。软删除不会伪造运营商通知。</p>}
    {archives?.map(a=><div key={`${a.entry.sequence_number}:${a.sha256}`} style={{marginTop:12,overflowWrap:'anywhere'}}>
      <div>ICCID {a.entry.iccid} · 通知 {a.entry.sequence_number}</div>
      <div>SHA-256 {a.sha256}</div>
      <ul>{a.attempts?.map(attempt=><li key={attempt.id}>{attempt.at} · {attempt.state}</li>)}</ul>
      <button className="btn btn-danger-outline" disabled={busy} onClick={()=>replay(a)}>确认后重发删除通知</button>
    </div>)}
  </section>
}
