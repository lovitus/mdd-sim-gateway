package com.lovitus.mddagent;

import android.Manifest;
import android.app.NotificationChannel;
import android.app.NotificationManager;
import android.content.Context;
import android.content.pm.PackageInfo;
import android.content.pm.PackageManager;
import android.net.ConnectivityManager;
import android.net.NetworkCapabilities;
import android.os.Build;
import android.os.PowerManager;
import org.json.JSONObject;
import java.util.LinkedHashMap;
import java.util.Map;

/** One allowlisted snapshot for both the local dialog and explicit sharing. */
final class AgentDiagnostics {
    private final LinkedHashMap<String,Object> facts=new LinkedHashMap<>();
    private final LinkedHashMap<String,Integer> labels=new LinkedHashMap<>();
    private void put(String key,int label,Object value){facts.put(key,value==null?JSONObject.NULL:value);labels.put(key,label);}
    static AgentDiagnostics capture(Context context,AgentService service){
        AgentDiagnostics result=new AgentDiagnostics();
        result.put("app_version",R.string.diagnostics_app,version(context));
        String revision=context.getString(R.string.source_revision);
        result.put("source_revision",R.string.diagnostics_revision,revision.matches("[0-9a-f]{40}")?revision:"unknown");
        result.put("android_api",R.string.diagnostics_android,Build.VERSION.SDK_INT);
        result.put("availability_intent",R.string.diagnostics_available,service==null?null:service.available());
        result.put("gateway_connected",R.string.diagnostics_gateway,service==null?null:service.online());
        result.put("reader_sharing_intent",R.string.diagnostics_sharing,service==null?null:service.sharing());
        result.put("reader_link_connected",R.string.diagnostics_reader_link,service==null?null:service.readerLinkOnline);
        result.put("reader_link_failure",R.string.diagnostics_reader_failure,service==null?null:service.readerLinkDiagnostic());
        result.put("reported_readers",R.string.diagnostics_readers,service==null?null:Json.array(service.readers(),"readers").length());
        result.put("network_transport",R.string.diagnostics_network,network(context));
        result.put("microphone_permission",R.string.diagnostics_microphone,context.checkSelfPermission(Manifest.permission.RECORD_AUDIO)==PackageManager.PERMISSION_GRANTED);
        try{
            NotificationManager manager=context.getSystemService(NotificationManager.class);
            result.put("notifications_enabled",R.string.diagnostics_notifications,manager.areNotificationsEnabled());
            NotificationChannel channel=manager.getNotificationChannel("incoming");
            result.put("event_channel_enabled",R.string.diagnostics_event_channel,channel==null?null:channel.getImportance()!=NotificationManager.IMPORTANCE_NONE);
        }catch(RuntimeException failure){result.put("notifications_enabled",R.string.diagnostics_notifications,null);result.put("event_channel_enabled",R.string.diagnostics_event_channel,null);}
        try{result.put("battery_optimization",R.string.diagnostics_battery,context.getSystemService(PowerManager.class).isIgnoringBatteryOptimizations(context.getPackageName())?"exempt":"enabled");}
        catch(RuntimeException failure){result.put("battery_optimization",R.string.diagnostics_battery,"unknown");}
        RemoteCall call=service==null?null:service.call;
        String phase=service==null?"unknown":call==null?"none":call.phase;
        switch(phase){case "none":case "PREPARING":case "START_MAY_HAVE_RUN":case "ACTIVE":case "ENDING":case "TERMINAL":break;default:phase="unknown";}
        result.put("local_call_phase",R.string.diagnostics_call,phase);
        return result;
    }
    static String version(Context context){
        try{PackageInfo info=context.getPackageManager().getPackageInfo(context.getPackageName(),0);return context.getString(R.string.version_label,info.versionName,info.getLongVersionCode());}
        catch(PackageManager.NameNotFoundException failure){return context.getString(R.string.unknown);}
    }
    private static String network(Context context){
        try{
            ConnectivityManager manager=context.getSystemService(ConnectivityManager.class);
            android.net.Network network=manager.getActiveNetwork();if(network==null)return "none";
            NetworkCapabilities capabilities=manager.getNetworkCapabilities(network);if(capabilities==null)return "unknown";
            if(capabilities.hasTransport(NetworkCapabilities.TRANSPORT_VPN))return "vpn";
            if(capabilities.hasTransport(NetworkCapabilities.TRANSPORT_WIFI))return "wifi";
            if(capabilities.hasTransport(NetworkCapabilities.TRANSPORT_CELLULAR))return "cellular";
            if(capabilities.hasTransport(NetworkCapabilities.TRANSPORT_ETHERNET))return "ethernet";
            return "other";
        }catch(RuntimeException failure){return "unknown";}
    }
    String display(Context context){
        StringBuilder result=new StringBuilder();
        for(Map.Entry<String,Object> item:facts.entrySet()){
            if(result.length()>0)result.append("\n\n");
            result.append(context.getString(labels.get(item.getKey()))).append("\n").append(displayValue(context,item.getValue()));
        }
        return result.toString();
    }
    private static String displayValue(Context context,Object value){
        if(value==JSONObject.NULL||value.equals("unknown"))return context.getString(R.string.diagnostics_unknown);
        if(value instanceof Boolean)return context.getString((Boolean)value?R.string.diagnostics_yes:R.string.diagnostics_no);
        switch(String.valueOf(value)){
            case "none":return context.getString(R.string.diagnostics_none);
            case "enabled":return context.getString(R.string.diagnostics_enabled);
            case "exempt":return context.getString(R.string.diagnostics_exempt);
            case "wifi":return "Wi-Fi";
            case "vpn":return "VPN";
            case "cellular":return context.getString(R.string.cellular);
            default:return String.valueOf(value);
        }
    }
    String share(){try{return new JSONObject(facts).toString(2);}catch(org.json.JSONException failure){throw new IllegalStateException("Diagnostic snapshot could not be encoded",failure);}}
}
