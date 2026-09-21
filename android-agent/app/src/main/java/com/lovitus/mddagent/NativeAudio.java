package com.lovitus.mddagent;
import android.Manifest;
import android.annotation.SuppressLint;
import android.content.Context;
import android.media.*;
import android.media.audiofx.*;
import android.os.SystemClock;
import okhttp3.*;
import okio.ByteString;
import org.json.*;
import java.io.IOException;
import java.util.concurrent.*;
import java.util.concurrent.atomic.*;
/** 8kHz PCM16LE, 20ms frames, bounded queues; existing Core canary + resume protocol. */
final class NativeAudio implements AutoCloseable {
    interface Events{void state(String state);void ended(String reason);}
    private final GatewayApi api;private final ScheduledExecutorService timer;private final Events events;
    private final AudioManager manager;private AudioRecord record;private AudioTrack track;private AcousticEchoCanceler echo;private NoiseSuppressor noise;
    private final ArrayBlockingQueue<byte[]> receive=new ArrayBlockingQueue<>(25);
    private final AtomicLong captured=new AtomicLong(),played=new AtomicLong(),callbacks=new AtomicLong();
    private final CompletableFuture<Void> ready=new CompletableFuture<>();
    private final AudioFocusRequest focus;private WebSocket socket;private String session,path,ticket,resumeTicket="",challenge="";private long connectionEpoch,epoch,recoverUntil,lastInbound;
    volatile boolean closed,started,active,muted;private ScheduledFuture<?> evidence,reconnect;
    @SuppressLint("MissingPermission")
    NativeAudio(Context context,GatewayApi api,ScheduledExecutorService timer,Events events)throws Exception{
        this.api=api;this.timer=timer;this.events=events;manager=context.getSystemService(AudioManager.class);
        AudioAttributes attributes=new AudioAttributes.Builder().setUsage(AudioAttributes.USAGE_VOICE_COMMUNICATION).setContentType(AudioAttributes.CONTENT_TYPE_SPEECH).build();
        focus=new AudioFocusRequest.Builder(AudioManager.AUDIOFOCUS_GAIN_TRANSIENT).setAudioAttributes(attributes).setOnAudioFocusChangeListener(change->{if(change==AudioManager.AUDIOFOCUS_LOSS||change==AudioManager.AUDIOFOCUS_LOSS_TRANSIENT)fail("Audio focus lost; call audio stopped");}).build();
        try{
            if(manager.requestAudioFocus(focus)!=AudioManager.AUDIOFOCUS_REQUEST_GRANTED)throw new IOException("Audio is in use by another call");
            manager.setMode(AudioManager.MODE_IN_COMMUNICATION);
            record=new AudioRecord(MediaRecorder.AudioSource.VOICE_COMMUNICATION,8000,AudioFormat.CHANNEL_IN_MONO,AudioFormat.ENCODING_PCM_16BIT,Math.max(3200,AudioRecord.getMinBufferSize(8000,AudioFormat.CHANNEL_IN_MONO,AudioFormat.ENCODING_PCM_16BIT)));
            track=new AudioTrack.Builder().setAudioAttributes(attributes).setAudioFormat(new AudioFormat.Builder().setSampleRate(8000).setChannelMask(AudioFormat.CHANNEL_OUT_MONO).setEncoding(AudioFormat.ENCODING_PCM_16BIT).build()).setBufferSizeInBytes(Math.max(3200,AudioTrack.getMinBufferSize(8000,AudioFormat.CHANNEL_OUT_MONO,AudioFormat.ENCODING_PCM_16BIT))).setTransferMode(AudioTrack.MODE_STREAM).build();
            if(record.getState()!=AudioRecord.STATE_INITIALIZED||track.getState()!=AudioTrack.STATE_INITIALIZED)throw new IOException("8 kHz voice audio unavailable");
            if(AcousticEchoCanceler.isAvailable()){echo=AcousticEchoCanceler.create(record.getAudioSessionId());if(echo!=null)echo.setEnabled(true);}
            if(NoiseSuppressor.isAvailable()){noise=NoiseSuppressor.create(record.getAudioSessionId());if(noise!=null)noise.setEnabled(true);}
            record.startRecording();track.play();
            Thread input=new Thread(this::capture,"mdd-microphone"),output=new Thread(this::playback,"mdd-playback");input.setDaemon(true);output.setDaemon(true);input.start();output.start();
        }catch(Exception e){close();throw e;}
    }
    CompletableFuture<Void> prepare(JSONObject lease,String ticket)throws Exception{
        session=lease.getString("session_id");path=lease.getString("ws_path");api.endpoint.path(path);this.ticket=ticket;
        evidence=timer.scheduleWithFixedDelay(this::evidence,100,250,TimeUnit.MILLISECONDS);
        timer.schedule(()->{if(!ready.isDone())fail("Bidirectional audio check timed out; no automatic dial");},20,TimeUnit.SECONDS);
        connect(false);return ready;
    }
    private void capture(){try{while(!closed){byte[] frame=new byte[320];int n=0;while(n<320&&!closed){int read=record.read(frame,n,320-n,AudioRecord.READ_BLOCKING);if(read<=0)throw new IOException();n+=read;captured.incrementAndGet();}
            WebSocket ws; synchronized(this){ws=socket;} if(!closed&&started&&ws!=null&&ws.queueSize()<=8000)ws.send(ByteString.of(muted?new byte[320]:frame));}}
        catch(Exception e){if(!closed)fail("Microphone stopped");}}
    private void playback(){try{while(!closed){byte[] f=receive.poll(20,TimeUnit.MILLISECONDS);boolean real=f!=null;if(!real)f=new byte[320];int n=track.write(f,0,320,AudioTrack.WRITE_BLOCKING);if(n!=320)throw new IOException();callbacks.incrementAndGet();if(real)played.incrementAndGet();}}
        catch(Exception e){if(!closed)fail("Playback stopped");}}
    private synchronized void connect(boolean resume){
        if(closed)return;long mine=++epoch;started=false;receive.clear();
        lastInbound=SystemClock.elapsedRealtime();
        socket=api.http.newWebSocket(api.request(path).build(),new WebSocketListener(){
            public void onOpen(WebSocket ws,Response r){synchronized(NativeAudio.this){if(!current(mine,ws))return;ws.send((resume?Json.obj("type","browser.media.resume","version",1,"session_id",session,"resume_ticket",resumeTicket,"connection_epoch",connectionEpoch):Json.obj("type","browser.media.hello","version",1,"session_id",session,"ticket",ticket)).toString());}}
            public void onMessage(WebSocket ws,ByteString bytes){synchronized(NativeAudio.this){if(!current(mine,ws))return;lastInbound=SystemClock.elapsedRealtime();if(bytes.size()!=320){fail("Invalid PCM frame");return;}if(!receive.offer(bytes.toByteArray())){receive.poll();receive.offer(bytes.toByteArray());}}}
            public void onMessage(WebSocket ws,String text){synchronized(NativeAudio.this){if(!current(mine,ws))return;try{lastInbound=SystemClock.elapsedRealtime();if(text.length()>16384)throw new IOException();JSONObject m=new JSONObject(text);String type=m.getString("type");
                if(type.equals("browser.media.claimed")||type.equals("browser.media.resumed")){challenge=m.getString("challenge");resumeTicket=m.getString("resume_ticket");connectionEpoch=m.getLong("connection_epoch");if(challenge.isEmpty()||resumeTicket.isEmpty()||connectionEpoch<1)throw new IOException();evidence();}
                else if(type.equals("browser.media.started")){String purpose=resume||active?"call":"canary";if(!purpose.equals(m.getString("purpose")))throw new IOException();started=true;if(resume)recoverUntil=0;events.state(resume?"Audio reconnected":"Checking audio");}
                else if(type.equals("browser.media.ready")||type.equals("browser.media.status")&&m.optBoolean("ready")){ready.complete(null);}
            }catch(Exception e){fail("Invalid media handshake");}}}
            public void onClosing(WebSocket ws,int code,String reason){ws.close(code,null);}
            public void onClosed(WebSocket ws,int code,String reason){synchronized(NativeAudio.this){if(!current(mine,ws))return;if(code==1000&&active){close();events.ended("Call ended");}else interrupted(mine);}}
            public void onFailure(WebSocket ws,Throwable e,Response r){synchronized(NativeAudio.this){if(current(mine,ws))interrupted(mine);}}
        });
    }
    private boolean current(long mine,WebSocket ws){return !closed&&epoch==mine&&socket==ws;}
    private synchronized void interrupted(long mine){if(epoch!=mine||closed)return;epoch++;WebSocket old=socket;socket=null;if(old!=null)old.cancel();started=false;receive.clear();
        if(active&&!resumeTicket.isEmpty()){long now=SystemClock.elapsedRealtime();if(recoverUntil==0)recoverUntil=now+8500;if(now<recoverUntil){events.state("Network interrupted; resuming same call");reconnect=timer.schedule(()->connect(true),500,TimeUnit.MILLISECONDS);return;}}
        fail("Media unavailable; the server call guard will clean up. No redial.");
    }
    void networkChanged(){synchronized(this){if(!closed)interrupted(epoch);}}
    private synchronized void evidence(){if(closed)return;
        long now=SystemClock.elapsedRealtime();
        if(recoverUntil>0&&now>=recoverUntil){fail("Media reconnect deadline exceeded; no redial");return;}
        if(active&&socket!=null&&now-lastInbound>5000){interrupted(epoch);return;}
        if(socket==null||challenge.isEmpty())return;socket.send(Json.obj("type","browser.media.evidence","version",1,"challenge",challenge,"capture_callbacks",captured.get(),"playback_callbacks",callbacks.get(),"played_frames",played.get()).toString());}
    synchronized void markActive(){if(closed)return;active=true;recoverUntil=0;events.state("Call active");}
    void mute(){muted=!muted;events.state(muted?"Microphone muted":"Microphone on");}
    void speaker(){manager.setSpeakerphoneOn(!manager.isSpeakerphoneOn());}
    private synchronized void fail(String reason){if(closed)return;ready.completeExceptionally(new IOException(reason));close();events.ended(reason);}
    public synchronized void close(){if(closed)return;closed=true;epoch++;started=false;if(evidence!=null)evidence.cancel(false);if(reconnect!=null)reconnect.cancel(false);if(socket!=null)socket.cancel();socket=null;receive.clear();ready.completeExceptionally(new IOException("Audio closed"));
        try{if(record!=null)record.stop();}catch(Exception ignored){}try{if(track!=null)track.stop();}catch(Exception ignored){}
        if(echo!=null)echo.release();if(noise!=null)noise.release();if(record!=null)record.release();if(track!=null)track.release();
        manager.setSpeakerphoneOn(false);manager.setMode(AudioManager.MODE_NORMAL);manager.abandonAudioFocusRequest(focus);
    }
}
