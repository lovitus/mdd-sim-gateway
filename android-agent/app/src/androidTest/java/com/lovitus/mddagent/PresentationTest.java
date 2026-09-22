package com.lovitus.mddagent;

import android.content.*;
import android.content.res.Configuration;
import android.content.pm.ActivityInfo;
import android.graphics.Rect;
import android.os.*;
import android.view.*;
import android.view.inputmethod.InputMethodManager;
import android.widget.*;
import androidx.test.core.app.*;
import androidx.test.platform.app.InstrumentationRegistry;
import androidx.test.ext.junit.runners.AndroidJUnit4;
import org.json.*;
import org.junit.Test;
import org.junit.Ignore;
import org.junit.runner.RunWith;
import java.util.*;
import java.util.concurrent.*;
import java.util.concurrent.atomic.*;
import static org.junit.Assert.*;

/** Real QA views and process-local resources; never changes phone language settings. */
@RunWith(AndroidJUnit4.class)
public class PresentationTest {
    private static final String EVIDENCE="/data/local/tmp/mdd-native-ui-presentation-"+UUID.randomUUID();
    private static Context context(){return ApplicationProvider.getApplicationContext();}
    private static AgentService service(MainActivity activity){try{java.lang.reflect.Field f=MainActivity.class.getDeclaredField("service");f.setAccessible(true);return (AgentService)f.get(activity);}catch(Exception failure){throw new AssertionError(failure);}}
    private static void render(MainActivity activity,Configuration config,int width){
        activity.getResources().updateConfiguration(config,activity.getResources().getDisplayMetrics());
        try{java.lang.reflect.Method build=MainActivity.class.getDeclaredMethod("build");build.setAccessible(true);build.invoke(activity);}catch(Exception failure){throw new AssertionError(failure);}
        View root=((ViewGroup)activity.findViewById(android.R.id.content)).getChildAt(0);
        ViewGroup.LayoutParams params=root.getLayoutParams();params.width=width;root.setLayoutParams(params);
    }
    private static void idle(){InstrumentationRegistry.getInstrumentation().waitForIdleSync();}
    private static boolean laidOut(MainActivity activity){View root=((ViewGroup)activity.findViewById(android.R.id.content)).getChildAt(0);return root.isLaidOut()&&!root.isLayoutRequested();}
    private static void await(ActivityScenario<MainActivity> scene,java.util.function.Predicate<MainActivity> condition)throws Exception{
        long until=SystemClock.elapsedRealtime()+15000;AtomicBoolean ready=new AtomicBoolean();
        while(!ready.get()&&SystemClock.elapsedRealtime()<until){idle();scene.onActivity(a->ready.set(condition.test(a)));if(!ready.get())SystemClock.sleep(100);}
        assertTrue("Presentation did not settle",ready.get());
    }
    private static void visible(View view){Rect bounds=new Rect();assertTrue(view.getGlobalVisibleRect(bounds));String identity=view.getResources().getResourceEntryName(view.getId());System.out.println("QA_VIEW_GEOMETRY "+Json.obj("view",identity,"width",view.getWidth(),"height",view.getHeight(),"visibleWidth",bounds.width(),"visibleHeight",bounds.height()));assertTrue(identity+" height must remain fully visible",bounds.height()>=view.getHeight());assertTrue(identity+" width must remain fully visible",bounds.width()>=view.getWidth());}
    private static void reveal(View view){view.requestRectangleOnScreen(new Rect(0,0,view.getWidth(),view.getHeight()),true);}
    private static void visibleInWindow(MainActivity activity,View view){
        visible(view);Rect usable=new Rect();activity.getWindow().getDecorView().getWindowVisibleDisplayFrame(usable);int[] location=new int[2];view.getLocationOnScreen(location);
        assertTrue("Primary action must be outside the IME and system occlusion",usable.contains(new Rect(location[0],location[1],location[0]+view.getWidth(),location[1]+view.getHeight())));
    }
    private static JSONObject resourceState(Context context){Configuration c=context.getResources().getConfiguration();return Json.obj("locale",c.getLocales().toLanguageTags(),"fontScale",c.fontScale);}
    private static void capture(String name)throws Exception{
        idle();for(String command:new String[]{"mkdir -p "+EVIDENCE,"screencap -p "+EVIDENCE+"/"+name+".png"}){
            ParcelFileDescriptor fd=InstrumentationRegistry.getInstrumentation().getUiAutomation().executeShellCommand(command);
            try(java.io.InputStream input=new ParcelFileDescriptor.AutoCloseInputStream(fd)){byte[] bytes=new byte[1024];while(input.read(bytes)!=-1){}}
        }
    }
    @Test public void fivePagesKeepLongIdentityDraftsAndLocalizedControls()throws Exception{
        assertTrue(context().getPackageName().endsWith(".qa"));
        Configuration original=new Configuration(context().getResources().getConfiguration());
        AtomicReference<AgentService> owner=new AtomicReference<>();AtomicReference<Configuration> serviceOriginal=new AtomicReference<>();
        try(SmsGatewayFixture fixture=new SmsGatewayFixture(context(),"vowifi")){
            fixture.line.put("name","Long gateway line name for a remote external reader").put("number","+441234567890123");
            try(ActivityScenario<MainActivity> scene=ActivityScenario.launch(MainActivity.class)){
                assertTrue(fixture.connected.await(10,TimeUnit.SECONDS));await(scene,a->service(a)!=null&&service(a).online());
                scene.onActivity(a->{owner.set(service(a));serviceOriginal.set(new Configuration(owner.get().getResources().getConfiguration()));});
                JSONObject scopeBefore=Json.obj("application",resourceState(context()),"service",resourceState(owner.get()));
                for(String language:new String[]{"en","zh-CN"})for(boolean large:new boolean[]{false,true}){
                    Configuration config=new Configuration(original);config.setLocale(Locale.forLanguageTag(language));config.fontScale=large?2f:1f;
                    scene.onActivity(a->render(a,config,large?Math.round(320*a.getResources().getDisplayMetrics().density):ViewGroup.LayoutParams.MATCH_PARENT));idle();
                    System.out.println("QA_RESOURCE_SCOPE "+Json.obj("requested",language,"large",large,"before",scopeBefore,"application",resourceState(context()),"service",resourceState(owner.get())));
                    int[] tabs={R.id.tab_home,R.id.tab_calls,R.id.tab_messages,R.id.tab_readers,R.id.tab_settings};
                    int[] titles={R.string.home,R.string.calls,R.string.messages,R.string.readers,R.string.settings};
                    for(int i=0;i<tabs.length;i++){
                        int tab=tabs[i],title=titles[i];scene.onActivity(a->a.findViewById(tab).performClick());await(scene,PresentationTest::laidOut);
                        scene.onActivity(a->{assertEquals("MDD · "+a.getString(title),((TextView)a.findViewById(R.id.page_title)).getText().toString());
                            if(tab==R.id.tab_calls||tab==R.id.tab_messages){Spinner selector=a.findViewById(R.id.line_selector);TextView selected=(TextView)selector.getSelectedView();assertNotNull(selected);assertTrue(selected.getText().toString().contains("+441234567890123"));assertNull(selected.getEllipsize());assertNotNull(selected.getLayout());assertTrue(selected.getLayout().getHeight()<=selected.getHeight()-selected.getPaddingTop()-selected.getPaddingBottom());}
                            if(tab==R.id.tab_calls){visible(a.findViewById(R.id.call_dial));((EditText)a.findViewById(R.id.dial_number)).setText("+15550100999");}
                            if(tab==R.id.tab_messages){assertEquals("+15550100999",((EditText)a.findViewById(R.id.dial_number)).getText().toString());((EditText)a.findViewById(R.id.message_body)).setText("retained fixture draft");visible(a.findViewById(R.id.message_send));}
                            if(tab==R.id.tab_settings)assertNotNull(a.findViewById(R.id.diagnostics_open));
                        });
                        capture(language+(large?"-large-":"-normal-")+i);
                        if(tab==R.id.tab_calls){
                            scene.onActivity(a->((Spinner)a.findViewById(R.id.line_selector)).performClick());idle();
                            android.view.accessibility.AccessibilityNodeInfo row=dropdownRow("+441234567890123");Rect area=new Rect();row.getBoundsInScreen(area);
                            capture(language+(large?"-large":"-normal")+"-dropdown");
                            scene.onActivity(a->{Spinner picker=a.findViewById(R.id.line_selector);View reference=picker.getAdapter().getDropDownView(picker.getSelectedItemPosition(),null,new FrameLayout(a));reference.measure(View.MeasureSpec.makeMeasureSpec(area.width(),View.MeasureSpec.EXACTLY),View.MeasureSpec.makeMeasureSpec(0,View.MeasureSpec.UNSPECIFIED));System.out.println("QA_DROPDOWN_GEOMETRY "+Json.obj("actualWidth",area.width(),"actualHeight",area.height(),"requiredHeight",reference.getMeasuredHeight(),"fontScale",a.getResources().getConfiguration().fontScale));assertTrue("Entire multiline dropdown row must fit its actual width",area.height()>=reference.getMeasuredHeight());});
                            shell("input tap "+area.centerX()+" "+area.centerY());idle();
                        }
                        if(tab==R.id.tab_settings){scene.onActivity(a->reveal(a.findViewById(R.id.sign_in_again)));await(scene,PresentationTest::laidOut);scene.onActivity(a->visible(a.findViewById(R.id.sign_in_again)));}
                    }
                    scene.onActivity(a->a.findViewById(R.id.tab_messages).performClick());idle();scene.onActivity(a->assertEquals("retained fixture draft",((EditText)a.findViewById(R.id.message_body)).getText().toString()));
                }
                assertEquals(0,fixture.sends.get());fixture.check();
            }finally{
                context().getResources().updateConfiguration(original,context().getResources().getDisplayMetrics());
                if(owner.get()!=null&&serviceOriginal.get()!=null){owner.get().getResources().updateConfiguration(serviceOriginal.get(),owner.get().getResources().getDisplayMetrics());assertEquals(serviceOriginal.get().getLocales(),owner.get().getResources().getConfiguration().getLocales());assertEquals(serviceOriginal.get().fontScale,owner.get().getResources().getConfiguration().fontScale,0.001f);}
                assertEquals(original.getLocales(),context().getResources().getConfiguration().getLocales());assertEquals(original.fontScale,context().getResources().getConfiguration().fontScale,0.001f);
                context().stopService(new Intent(context(),AgentService.class));idle();
            }
        }
    }
    @Test public void diagnosticsUseOnlyWhitelistedFactsAndTextResolvesAtRenderTime()throws Exception{
        assertTrue(context().getPackageName().endsWith(".qa"));
        try(SmsGatewayFixture fixture=new SmsGatewayFixture(context(),"vowifi");ActivityScenario<MainActivity> scene=ActivityScenario.launch(MainActivity.class)){
            await(scene,a->service(a)!=null&&service(a).online());
            UiText message=UiText.of(R.string.call_saving_recovery,UiText.of(R.string.call_remote_ended));
            Configuration en=new Configuration(context().getResources().getConfiguration());en.setLocale(Locale.ENGLISH);
            Configuration zh=new Configuration(en);zh.setLocale(Locale.SIMPLIFIED_CHINESE);
            assertNotEquals(message.render(context().createConfigurationContext(en)),message.render(context().createConfigurationContext(zh)));
            AtomicReference<AgentDiagnostics> diagnostics=new AtomicReference<>();
            scene.onActivity(a->{AgentService owner=service(a);owner.notice=UiText.of(R.string.call_reason,UiText.of(R.string.state_unknown),"PRIVATE-ERROR-MARKER");diagnostics.set(AgentDiagnostics.capture(a,owner));});
            JSONObject shared=new JSONObject(diagnostics.get().share());Set<String> keys=new HashSet<>();shared.keys().forEachRemaining(keys::add);
            assertEquals(new HashSet<>(Arrays.asList("app_version","source_revision","android_api","availability_intent","gateway_connected","reader_sharing_intent","reported_readers","network_transport","microphone_permission","notifications_enabled","event_channel_enabled","battery_optimization","local_call_phase")),keys);
            String encoded=shared.toString();for(String secret:new String[]{"PRIVATE-ERROR-MARKER","fixture-session","fixture-csrf",fixture.card,fixture.gateway.url("/").toString()})assertFalse("Diagnostic leak",encoded.contains(secret));
            for(Context localized:new Context[]{context().createConfigurationContext(en),context().createConfigurationContext(zh)}){
                assertEquals(localized.getString(R.string.message_delivered),UiLabels.messageEvent(localized,Json.obj("kind","delivery","state","delivered")));
                assertEquals(localized.getString(R.string.message_failed),UiLabels.messageEvent(localized,Json.obj("kind","delivery","state","failed")));
                assertEquals(localized.getString(R.string.message_part,2,localized.getString(R.string.message_submitted)),UiLabels.messageEvent(localized,Json.obj("kind","submitted","part",2)));
            }
            scene.onActivity(a->{assertTrue(diagnostics.get().display(a).contains(a.getString(R.string.diagnostics_gateway)));a.findViewById(R.id.tab_settings).performClick();});idle();
            scene.onActivity(a->reveal(a.findViewById(R.id.diagnostics_open)));idle();scene.onActivity(a->{visible(a.findViewById(R.id.diagnostics_open));a.findViewById(R.id.diagnostics_open).performClick();});idle();capture("diagnostics");assertEquals(0,fixture.sends.get());fixture.check();
        }finally{context().stopService(new Intent(context(),AgentService.class));idle();}
    }
    @Ignore("Owner decision 2026-09-23: landscape compatibility deferred until explicitly requested")
    @Test public void landscapeKeyboardAndActiveCallKeepThePrimaryActionReachable()throws Exception{
        assertTrue(context().getPackageName().endsWith(".qa"));Configuration original=new Configuration(context().getResources().getConfiguration());
        shell("pm grant "+context().getPackageName()+" android.permission.RECORD_AUDIO");
        if(Build.VERSION.SDK_INT>=33)shell("pm grant "+context().getPackageName()+" android.permission.POST_NOTIFICATIONS");
        try(CallGatewayFixture fixture=new CallGatewayFixture(context(),"vowifi",false)){
            fixture.line.put("name","Long display-only identity for a remote gateway SIM").put("number","+441234567890123");
            try(ActivityScenario<MainActivity> scene=ActivityScenario.launch(MainActivity.class)){
                await(scene,a->service(a)!=null&&service(a).online());scene.onActivity(a->a.setRequestedOrientation(ActivityInfo.SCREEN_ORIENTATION_LANDSCAPE));
                await(scene,a->a.getResources().getConfiguration().orientation==Configuration.ORIENTATION_LANDSCAPE);
                scene.onActivity(a->{Configuration config=new Configuration(a.getResources().getConfiguration());config.setLocale(Locale.SIMPLIFIED_CHINESE);config.fontScale=1;render(a,config,ViewGroup.LayoutParams.MATCH_PARENT);a.findViewById(R.id.tab_calls).performClick();});
                await(scene,a->a.findViewById(R.id.call_dial)!=null&&a.findViewById(R.id.call_dial).isEnabled());
                scene.onActivity(a->{EditText input=a.findViewById(R.id.dial_number);input.requestFocus();a.getSystemService(InputMethodManager.class).showSoftInput(input,InputMethodManager.SHOW_IMPLICIT);});
                await(scene,PresentationTest::keyboardVisible);scene.onActivity(a->visibleInWindow(a,a.findViewById(R.id.call_dial)));capture("landscape-keyboard-100pct");
                scene.onActivity(a->{EditText input=a.findViewById(R.id.dial_number);a.getSystemService(InputMethodManager.class).hideSoftInputFromWindow(input.getWindowToken(),0);input.clearFocus();input.setText("+15550100999");});
                await(scene,a->!keyboardVisible(a));scene.onActivity(a->a.findViewById(R.id.call_dial).performClick());idle();
                clickDialog("android:id/button1");assertTrue(fixture.started.await(15,TimeUnit.SECONDS));
                await(scene,a->a.findViewById(R.id.call_mute)!=null&&service(a).call.audio.active);
                scene.onActivity(a->{visibleInWindow(a,a.findViewById(R.id.call_hangup));visible(a.findViewById(R.id.call_mute));visible(a.findViewById(R.id.call_speaker));assertFalse(a.findViewById(R.id.call_mute).getContentDescription().toString().isEmpty());assertTrue(((TextView)a.findViewById(R.id.call_line_identity)).getText().toString().contains("+441234567890123"));});
                capture("landscape-call-100pct");scene.onActivity(a->a.findViewById(R.id.call_hangup).performClick());await(scene,a->service(a).call==null);
                assertEquals(1,fixture.starts.get());assertEquals(1,fixture.ends.get());assertEquals(1,fixture.leases.get());fixture.check();
                scene.onActivity(a->a.setRequestedOrientation(ActivityInfo.SCREEN_ORIENTATION_UNSPECIFIED));
            }finally{
                context().stopService(new Intent(context(),AgentService.class));idle();
                fixture.store.update(state->state.remove("pending_call"));fixture.store.clear();context().getResources().updateConfiguration(original,context().getResources().getDisplayMetrics());
            }
        }
    }
    private static boolean keyboardVisible(MainActivity activity){
        View decor=activity.getWindow().getDecorView();
        if(Build.VERSION.SDK_INT>=30)return decor.getRootWindowInsets()!=null&&decor.getRootWindowInsets().isVisible(WindowInsets.Type.ime());
        Rect frame=new Rect();decor.getWindowVisibleDisplayFrame(frame);return decor.getHeight()-frame.height()>100*activity.getResources().getDisplayMetrics().density;
    }
    private static android.view.accessibility.AccessibilityNodeInfo dropdownRow(String number)throws Exception{
        android.app.UiAutomation automation=InstrumentationRegistry.getInstrumentation().getUiAutomation();long until=SystemClock.elapsedRealtime()+5000;
        while(SystemClock.elapsedRealtime()<until){android.view.accessibility.AccessibilityNodeInfo root=automation.getRootInActiveWindow();if(root!=null){assertEquals(context().getPackageName(),String.valueOf(root.getPackageName()));for(android.view.accessibility.AccessibilityNodeInfo row:root.findAccessibilityNodeInfosByText(number))if("android.widget.CheckedTextView".equals(String.valueOf(row.getClassName()))&&row.isVisibleToUser())return row;}SystemClock.sleep(100);}
        throw new AssertionError("The actual multiline dropdown row was not visible");
    }
    private static void clickDialog(String id)throws Exception{
        android.app.UiAutomation automation=InstrumentationRegistry.getInstrumentation().getUiAutomation();
        android.accessibilityservice.AccessibilityServiceInfo flags=automation.getServiceInfo();flags.flags|=android.accessibilityservice.AccessibilityServiceInfo.FLAG_REPORT_VIEW_IDS;automation.setServiceInfo(flags);
        long until=SystemClock.elapsedRealtime()+5000;android.view.accessibility.AccessibilityNodeInfo selected=null;
        while(selected==null&&SystemClock.elapsedRealtime()<until){android.view.accessibility.AccessibilityNodeInfo root=automation.getRootInActiveWindow();if(root!=null){assertEquals(context().getPackageName(),String.valueOf(root.getPackageName()));java.util.List<android.view.accessibility.AccessibilityNodeInfo> rows=root.findAccessibilityNodeInfosByViewId(id);if(rows.size()==1&&rows.get(0).isEnabled())selected=rows.get(0);}if(selected==null)SystemClock.sleep(100);}
        assertNotNull(selected);assertTrue(selected.performAction(android.view.accessibility.AccessibilityNodeInfo.ACTION_CLICK));idle();
    }
    private static void shell(String command)throws Exception{
        ParcelFileDescriptor fd=InstrumentationRegistry.getInstrumentation().getUiAutomation().executeShellCommand(command);
        try(java.io.InputStream input=new ParcelFileDescriptor.AutoCloseInputStream(fd)){byte[] bytes=new byte[512];while(input.read(bytes)!=-1){}}
    }
}
