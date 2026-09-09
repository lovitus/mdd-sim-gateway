import React, {useEffect, useRef, useState} from 'react'
import {api} from '../api.js'
import {useI18n} from '../i18n.jsx'
import {diagnosticReport, runAdvancedDiagnostics} from '../advancedDiagnostics.js'

export default function AdvancedDiagnostics({instances=[], callCoordinator, subscribe}) {
  const {language}=useI18n()
  const zh=language==='zh'
  const [rows,setRows]=useState([])
  const [running,setRunning]=useState(false)
  const [outcome,setOutcome]=useState('not_run')
  const [lineID,setLineID]=useState('')
  const [filter,setFilter]=useState('all')
  const owner=useRef(null)
  const stateSeen=useRef(false)
  const coordinator=useRef(callCoordinator);coordinator.current=callCoordinator
  useEffect(()=>subscribe?.(message=>{if(message.type==='go.snapshot')stateSeen.current=true}),[subscribe])
  useEffect(()=>()=>{if(owner.current){owner.current.cancelled=true;if(owner.current.media)coordinator.current?.cancelMediaTest?.()}},[])
  const add=(row)=>setRows(previous=>[...previous.filter(item=>item.group!==row.group||item.id!==row.id),row])
  const cancel=()=>{if(owner.current){owner.current.cancelled=true;if(owner.current.media)coordinator.current?.cancelMediaTest?.();setOutcome('cancelled')}}
  const run=async()=>{
    if(owner.current)return
    const job={cancelled:false};owner.current=job;setRunning(true);setRows([]);setOutcome('running')
    try {
      await runAdvancedDiagnostics(api.advancedDiagnosticRead,{
        secure_context:globalThis.isSecureContext===true,websocket:typeof WebSocket==='function',
        audio_worklet:typeof AudioWorkletNode==='function',microphone_api:!!navigator.mediaDevices?.getUserMedia,
        state_stream_observed:stateSeen.current,
      },add,()=>job.cancelled)
      if(!job.cancelled)setOutcome('completed')
    }finally{if(owner.current===job)owner.current=null;setRunning(false)}
  }
  const media=async()=>{
    if(owner.current||!lineID)return
    const job={cancelled:false,media:true};owner.current=job;setRunning(true);setOutcome('running')
    try {
      const result=await coordinator.current.verifyMedia(lineID)
      if(!job.cancelled){add({group:'browser',id:'media',kind:'active_test',status:result?.cancelled?'skipped':'pass',code:result?.cancelled?'cancelled':'bidirectional_media_verified',observed_at:new Date().toISOString()});setOutcome(result?.cancelled?'cancelled':'completed')}
    }catch(error){if(!job.cancelled){add({group:'browser',id:'media',kind:'active_test',status:'fail',code:'media_test_failed',detail:error.message,observed_at:new Date().toISOString()});setOutcome('completed')}}
    finally{if(owner.current===job)owner.current=null;setRunning(false)}
  }
  const download=()=>{
    const url=URL.createObjectURL(new Blob([JSON.stringify(diagnosticReport(rows,outcome),null,2)],{type:'application/json'}))
    const link=document.createElement('a');link.href=url;link.download='mdd-diagnostics.json';link.click();setTimeout(()=>URL.revokeObjectURL(url),1000)
  }
  const statusNames=zh?{pass:'通过',fail:'异常',unknown:'未确认',skipped:'跳过'}:{pass:'Pass',fail:'Failed',unknown:'Unknown',skipped:'Skipped'}
  const groups=zh?{browser:'浏览器',core:'服务器',agent:'Agent',component:'组件',line:'线路'}:{browser:'Browser',core:'Server',agent:'Agent',component:'Component',line:'Line'}
  const kinds=zh?{capability:'能力检测',read:'接口读取',configuration:'配置',observation:'状态快照',active_test:'主动测试'}:{}
  const selected=instances.find(line=>String(line.id)===lineID)
  return <section>
    <div className="u-inline" style={{flexWrap:'wrap',gap:8}}>
      <button className="btn btn-primary" disabled={running} onClick={run}>{zh?'运行只读检查':'Run read-only checks'}</button>
      <button className="btn btn-ghost" disabled={!running||outcome==='cancelled'} onClick={cancel}>{zh?'取消':'Cancel'}</button>
      <button className="btn btn-ghost" disabled={running||!rows.length} onClick={download}>{zh?'导出脱敏报告':'Export redacted report'}</button>
      <span role="status">{zh?({not_run:'未运行',running:'运行中',completed:'检查结束',cancelled:'已取消'}[outcome]):outcome}</span>
    </div>
    <div className="u-inline" style={{flexWrap:'wrap',gap:8,marginTop:16}}>
      <select aria-label={zh?'媒体测试线路':'Media test line'} value={lineID} disabled={running} onChange={event=>setLineID(event.target.value)} style={{maxWidth:'100%'}}>
        <option value="">{zh?'选择线路':'Select line'}</option>
        {instances.map(line=><option key={line.id} value={line.id}>{line.name||line.id}</option>)}
      </select>
      <button className="btn btn-ghost" disabled={running||!selected?.operations?.vowifi_call?.ready||!callCoordinator?.verifyMedia} onClick={media}>{zh?'免费双向媒体测试':'No-charge bidirectional media test'}</button>
    </div>
    <div className="u-inline" style={{flexWrap:'wrap',gap:16,margin:'16px 0'}}>
      {Object.entries(statusNames).map(([key,name])=><span key={key}>{name}: {rows.filter(row=>row.status===key).length}</span>)}
      <select aria-label={zh?'诊断范围':'Diagnostic scope'} value={filter} onChange={event=>setFilter(event.target.value)}><option value="all">{zh?'全部':'All'}</option>{Object.entries(groups).map(([key,name])=><option key={key} value={key}>{name}</option>)}</select>
    </div>
    <div style={{overflowX:'auto'}}><table style={{width:'100%',minWidth:680,borderCollapse:'collapse',fontSize:13}}>
      <thead><tr>{(zh?['范围','检查项','类型','结果','状态码']:['Scope','Check','Type','Result','Code']).map(name=><th key={name} style={{textAlign:'left',padding:8}}>{name}</th>)}</tr></thead>
      <tbody>{rows.filter(row=>filter==='all'||row.group===filter).map(row=><tr key={`${row.group}:${row.id}`}>
        <td style={{padding:8,whiteSpace:'nowrap'}}>{groups[row.group]}</td><td style={{padding:8,overflowWrap:'anywhere'}}>{row.id}</td><td style={{padding:8,whiteSpace:'nowrap'}}>{kinds[row.kind]||row.kind}</td><td style={{padding:8,whiteSpace:'nowrap'}} className={row.status==='fail'?'u-error':''}>{statusNames[row.status]}</td><td style={{padding:8,overflowWrap:'anywhere'}}>{row.code}{row.detail&&<div>{row.detail}</div>}</td>
      </tr>)}</tbody>
    </table></div>
  </section>
}
