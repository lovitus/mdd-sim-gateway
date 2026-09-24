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
    interface Events{void state(int label);void ended(int label);}
    private final GatewayApi api;private final ScheduledExecutorService timer;private final Events events;
    private final Context context;
    private final AudioManager manager;private AudioRecord record;private AudioTrack track;private AcousticEchoCanceler echo;private NoiseSuppressor noise;
    private final ArrayBlockingQueue<byte[]> receive=new ArrayBlockingQueue<>(25);
    private final AtomicLong captured=new AtomicLong(),played=new AtomicLong(),callbacks=new AtomicLong();
    private final CompletableFuture<Void> ready=new CompletableFuture<>();
    private final AudioFocusRequest focus;private WebSocket socket;private String session,path,ticket,resumeTicket="",challenge="";private long connectionEpoch,epoch,recoverUntil,lastInbound;
    volatile boolean closed,started,active,muted;private volatile boolean focusSuspended;private volatile long focusEpoch;private ScheduledFuture<?> evidence,reconnect;
    volatile int closedReason=R.string.audio_off;
    @SuppressLint("MissingPermission")
    NativeAudio(Context context,GatewayApi api,ScheduledExecutorService timer,Events events)throws Exception{
        this.context=context;this.api=api;this.timer=timer;this.events=events;manager=context.getSystemService(AudioManager.class);
        AudioAttributes attributes=new AudioAttributes.Builder().setUsage(AudioAttributes.USAGE_VOICE_COMMUNICATION).setContentType(AudioAttributes.CONTENT_TYPE_SPEECH).build();
        focus=new AudioFocusRequest.Builder(AudioManager.AUDIOFOCUS_GAIN_TRANSIENT).setAudioAttributes(attributes).setOnAudioFocusChangeListener(this::audioFocusChanged).build();
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
        timer.schedule(()->{if(!ready.isDone())fail(R.string.audio_check_timeout);},20,TimeUnit.SECONDS);
        connect(false);return ready;
    }
    private void capture(){
        while(!closed){
            if(focusSuspended){SystemClock.sleep(20);continue;}
            long ownerEpoch=focusEpoch;
            try{if(record.getRecordingState()!=AudioRecord.RECORDSTATE_RECORDING)record.startRecording();byte[] frame=new byte[320];int n=0;while(n<320&&!closed){int read=record.read(frame,n,320-n,AudioRecord.READ_BLOCKING);if(read<=0){if(focusSuspended||focusEpoch!=ownerEpoch)break;throw new IOException();}n+=read;captured.incrementAndGet();}
                synchronized(this){WebSocket ws=socket;if(!closed&&!focusSuspended&&started&&n==320&&ws!=null&&ws.queueSize()<=8000)ws.send(ByteString.of(muted?new byte[320]:frame));}
            }catch(Exception error){if(closed)return;if(focusSuspended||focusEpoch!=ownerEpoch)continue;fail(R.string.audio_capture_stopped);return;}
        }
    }
    private void playback(){
        while(!closed){
            if(focusSuspended){SystemClock.sleep(20);continue;}
            long ownerEpoch=focusEpoch;
            try{if(track.getPlayState()!=AudioTrack.PLAYSTATE_PLAYING)track.play();byte[] frame=receive.poll(20,TimeUnit.MILLISECONDS);if(focusSuspended)continue;boolean real=frame!=null;if(!real)frame=new byte[320];int n=track.write(frame,0,320,AudioTrack.WRITE_BLOCKING);if(n!=320){if(focusSuspended||focusEpoch!=ownerEpoch)continue;throw new IOException();}callbacks.incrementAndGet();if(real)played.incrementAndGet();
            }catch(Exception error){if(closed)return;if(focusSuspended||focusEpoch!=ownerEpoch)continue;fail(R.string.audio_playback_stopped);return;}
        }
    }
    void audioFocusChanged(int change){
        if(change==AudioManager.AUDIOFOCUS_LOSS){fail(R.string.audio_focus_lost);return;}
        if(change==AudioManager.AUDIOFOCUS_LOSS_TRANSIENT||change==AudioManager.AUDIOFOCUS_LOSS_TRANSIENT_CAN_DUCK){
            synchronized(this){if(closed||focusSuspended)return;focusSuspended=true;focusEpoch++;receive.clear();try{if(record.getRecordingState()==AudioRecord.RECORDSTATE_RECORDING)record.stop();if(track.getPlayState()==AudioTrack.PLAYSTATE_PLAYING){track.pause();track.flush();}}catch(RuntimeException failure){fail(R.string.audio_focus_pause_failed);return;}}
            events.state(R.string.audio_focus_suspended);return;
        }
        if(change==AudioManager.AUDIOFOCUS_GAIN||change==AudioManager.AUDIOFOCUS_GAIN_TRANSIENT||change==AudioManager.AUDIOFOCUS_GAIN_TRANSIENT_EXCLUSIVE||change==AudioManager.AUDIOFOCUS_GAIN_TRANSIENT_MAY_DUCK){
            synchronized(this){if(closed||!focusSuspended)return;focusEpoch++;receive.clear();focusSuspended=false;}
            events.state(R.string.audio_focus_resumed);
        }
    }
    boolean focusSuspended(){return focusSuspended;}
    private synchronized void connect(boolean resume){
        if(closed)return;long mine=++epoch;started=false;receive.clear();
        // Evidence belongs to a claimed connection, never to a queued handshake.
        challenge="";
        lastInbound=SystemClock.elapsedRealtime();
        socket=api.http.newWebSocket(api.request(path).build(),new WebSocketListener(){
            public void onOpen(WebSocket ws,Response r){synchronized(NativeAudio.this){if(!current(mine,ws))return;ws.send((resume?Json.obj("type","browser.media.resume","version",1,"session_id",session,"resume_ticket",resumeTicket,"connection_epoch",connectionEpoch):Json.obj("type","browser.media.hello","version",1,"session_id",session,"ticket",ticket)).toString());}}
            public void onMessage(WebSocket ws,ByteString bytes){synchronized(NativeAudio.this){if(!current(mine,ws))return;lastInbound=SystemClock.elapsedRealtime();if(bytes.size()!=320){fail(R.string.audio_invalid_frame);return;}if(focusSuspended)return;if(!receive.offer(bytes.toByteArray())){receive.poll();receive.offer(bytes.toByteArray());}}}
            public void onMessage(WebSocket ws,String text){synchronized(NativeAudio.this){if(!current(mine,ws))return;try{lastInbound=SystemClock.elapsedRealtime();if(text.length()>16384)throw new IOException();JSONObject m=new JSONObject(text);String type=m.getString("type");
                if(type.equals("browser.media.claimed")||type.equals("browser.media.resumed")){challenge=m.getString("challenge");resumeTicket=m.getString("resume_ticket");connectionEpoch=m.getLong("connection_epoch");if(challenge.isEmpty()||resumeTicket.isEmpty()||connectionEpoch<1)throw new IOException();evidence();}
                else if(type.equals("browser.media.started")){String purpose=resume||active?"call":"canary";if(!purpose.equals(m.getString("purpose")))throw new IOException();started=true;if(resume)recoverUntil=0;events.state(resume?R.string.audio_reconnected:R.string.audio_checking);}
                else if(type.equals("browser.media.ready")||type.equals("browser.media.status")&&m.optBoolean("ready")){ready.complete(null);}
            }catch(Exception e){fail(R.string.audio_invalid_handshake);}}}
            public void onClosing(WebSocket ws,int code,String reason){ws.close(code,null);}
            public void onClosed(WebSocket ws,int code,String reason){synchronized(NativeAudio.this){if(!current(mine,ws))return;if(code==1000&&active){closedReason=R.string.audio_transport_closed;close();deliverEnded(closedReason);}else interrupted(mine);}}
            public void onFailure(WebSocket ws,Throwable e,Response r){synchronized(NativeAudio.this){if(current(mine,ws))interrupted(mine);}}
        });
    }
    private boolean current(long mine,WebSocket ws){return !closed&&epoch==mine&&socket==ws;}
    private synchronized void interrupted(long mine){if(epoch!=mine||closed)return;epoch++;WebSocket old=socket;socket=null;if(old!=null)old.cancel();started=false;receive.clear();
        if(active&&!resumeTicket.isEmpty()){long now=SystemClock.elapsedRealtime();if(recoverUntil==0)recoverUntil=now+8500;if(now<recoverUntil){events.state(R.string.audio_reconnecting);reconnect=timer.schedule(()->connect(true),500,TimeUnit.MILLISECONDS);return;}}
        fail(R.string.audio_lost);
    }
    void networkChanged(){synchronized(this){if(!closed)interrupted(epoch);}}
    private synchronized void evidence(){if(closed)return;
        long now=SystemClock.elapsedRealtime();
        if(recoverUntil>0&&now>=recoverUntil){fail(R.string.audio_reconnect_expired);return;}
        if(active&&socket!=null&&now-lastInbound>5000){interrupted(epoch);return;}
        if(socket==null||challenge.isEmpty())return;socket.send(Json.obj("type","browser.media.evidence","version",1,"challenge",challenge,"capture_callbacks",captured.get(),"playback_callbacks",callbacks.get(),"played_frames",played.get()).toString());}
    synchronized void markActive(){if(closed)return;active=true;recoverUntil=0;events.state(R.string.audio_connected);}
    void mute(){muted=!muted;events.state(muted?R.string.audio_muted:R.string.audio_unmuted);}
    boolean speakerEnabled(){return manager.isSpeakerphoneOn();}
    void speaker(){manager.setSpeakerphoneOn(!manager.isSpeakerphoneOn());events.state(manager.isSpeakerphoneOn()?R.string.audio_speaker:R.string.audio_earpiece);}
    private void deliverEnded(int label){try{timer.execute(()->events.ended(label));}catch(RejectedExecutionException ignored){/* The owning Service is already shutting down and closes its call resources. */}}
    private synchronized void fail(int label){if(closed)return;closedReason=label;ready.completeExceptionally(new IOException(context.getString(label)));close();deliverEnded(label);}
    UiText description(){
        if(closed)return UiText.of(closedReason);
        if(focusSuspended)return UiText.of(R.string.audio_focus_suspended);
        if(!started)return UiText.of(active?R.string.audio_reconnecting:R.string.audio_checking);
        if(!active)return UiText.of(R.string.audio_checking);
        return UiText.of(R.string.audio_live_description,UiText.of(muted?R.string.audio_muted:R.string.audio_unmuted),UiText.of(speakerEnabled()?R.string.audio_speaker:R.string.audio_earpiece));
    }
    public synchronized void close(){if(closed)return;closed=true;epoch++;started=false;if(evidence!=null)evidence.cancel(false);if(reconnect!=null)reconnect.cancel(false);if(socket!=null)socket.cancel();socket=null;receive.clear();ready.completeExceptionally(new IOException("Audio closed"));
        try{if(record!=null)record.stop();}catch(Exception ignored){}try{if(track!=null)track.stop();}catch(Exception ignored){}
        if(echo!=null)echo.release();if(noise!=null)noise.release();if(record!=null)record.release();if(track!=null)track.release();
        manager.setSpeakerphoneOn(false);manager.setMode(AudioManager.MODE_NORMAL);manager.abandonAudioFocusRequest(focus);
    }
}
