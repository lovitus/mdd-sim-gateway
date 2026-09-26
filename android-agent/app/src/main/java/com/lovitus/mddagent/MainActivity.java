package com.lovitus.mddagent;
import android.Manifest;
import android.app.*;
import android.content.*;
import android.content.pm.PackageManager;
import android.hardware.usb.*;
import android.net.Uri;
import android.os.*;
import android.provider.Settings;
import android.text.InputType;
import android.view.*;
import android.widget.*;
import android.content.res.ColorStateList;
import com.google.android.material.button.MaterialButton;
import com.google.android.material.button.MaterialButtonToggleGroup;
import com.google.android.material.badge.BadgeDrawable;
import com.google.android.material.bottomnavigation.BottomNavigationView;
import com.google.android.material.navigation.NavigationBarView;
import com.google.android.material.textfield.TextInputLayout;
import com.google.android.material.textfield.TextInputEditText;
import com.google.zxing.integration.android.*;
import org.json.*;
import java.util.*;
import java.util.concurrent.*;

/** Native, large-target UI. No WebView, injected JS or arbitrary APDU console. */
public final class MainActivity extends Activity {
    private AgentService service;private boolean bound,renderedAudioControls;private LinearLayout page,content,callBox,primaryBar;private TextView status,notice,pageTitle;private BottomNavigationView navigation;private RemoteCall renderedCall;
    private String focusIncoming="";private int tab;private EditText server,pin,username,password,number,message;private Spinner lineChoice;private MaterialButtonToggleGroup routeChoice;
    private TextView readerSummary,directoryStatus;private LinearLayout readerItems,usbPermissions;private String usbPermissionState="",readerItemsState="";private Button sharingButton;private TextView capability;private JSONObject lastData;private LinearLayout messageList,messageOperations;private String messageOperationView="";private JSONObject pinnedLine;private long historyEpoch,historyAccountEpoch;private AlertDialog activeHistoryDialog;
    private TextView homeConnection,homeAvailability,homeReaders,homeLines,homeActivity,homeUsbState,readerUsbState;private LinearLayout homeEvents;private long homeEventsRevision=-1;
    private JSONObject savedConfig=new JSONObject();private String storageError="";private boolean storageLoaded,startRequested;
    private LoginProfile loginProfile;private CheckBox rememberLogin;private TextView draftStatus;private boolean certificateExpanded;private volatile boolean loginBusy;private volatile long loginVersion;
    private volatile GatewayApi loginRequest;
    private String callSurfaceKey="",messageViewKey="";
    private boolean selectionFromState,selectionInitialized;
    private final ArrayList<String> renderedLineLabels=new ArrayList<>();
    private TextView callAudioDetails;
    private AlertDialog toneDialog;private RemoteCall toneOwner;private TextView toneStatus;private GridLayout toneKeys;
    private ScrollView contentScroll;private int renderedTab=-1;
    private final Handler draftHandler=new Handler(Looper.getMainLooper());private final Runnable saveDraft=this::persistLoginDraft;
    private String selectedLine="",selectedCard="",draftNumber="",draftMessage="";private int selectedRoute;private JSONArray selectionSnapshot=new JSONArray();private final ExecutorService io=Executors.newSingleThreadExecutor();private final Runnable update=this::updateState;
    private final ServiceConnection binding=new ServiceConnection(){public void onServiceConnected(ComponentName n,IBinder b){service=((AgentService.LocalBinder)b).service();service.addListener(update);render();resumeSavedAvailability();}public void onServiceDisconnected(ComponentName n){service=null;updateState();}};
    @Override public void onCreate(Bundle state){super.onCreate(state);Object retained=getLastNonConfigurationInstance();if(retained instanceof LoginProfile)loginProfile=(LoginProfile)retained;if(state!=null){selectionFromState=state.containsKey("route")||state.containsKey("line")||state.containsKey("card");tab=state.getInt("tab");focusIncoming=state.getString("incoming","");draftNumber=state.getString("number","");draftMessage=state.getString("message","");selectedLine=state.getString("line","");selectedCard=state.getString("card","");selectedRoute=state.getInt("route");}else readNotification(getIntent());if(Build.VERSION.SDK_INT>=30)getWindow().setDecorFitsSystemWindows(false);getWindow().setSoftInputMode(WindowManager.LayoutParams.SOFT_INPUT_ADJUST_RESIZE);build();}
    @Override public Object onRetainNonConfigurationInstance(){captureLoginDraft();return loginProfile;}
    private void readNotification(Intent intent){if(intent==null)return;String event=intent.getStringExtra("incoming_event");if(event!=null){focusIncoming=event;tab=1;}if(intent.getBooleanExtra("open_messages",false))tab=2;intent.removeExtra("incoming_event");intent.removeExtra("open_messages");}
    @Override protected void onNewIntent(Intent intent){super.onNewIntent(intent);readNotification(intent);render();}
    @Override protected void onStart(){super.onStart();bound=bindService(new Intent(this,AgentService.class),binding,BIND_AUTO_CREATE);reloadStorage();}
    private void resumeSavedAvailability(){if(storageLoaded&&storageError.isEmpty()&&savedConfig.optBoolean("available")&&service!=null&&!service.available()&&!service.hasLoadedConfiguration()&&!startRequested){startRequested=true;startForegroundService(new Intent(this,AgentService.class).setAction(AgentService.RESTORE));}}
    private void reloadStorage(){ConfigStore.intent(()->{try{JSONObject loaded=new ConfigStore(this).retry();runOnUiThread(()->{if(isDestroyed())return;savedConfig=loaded;restoreCallSelection(loaded);if(loginProfile==null)loginProfile=LoginProfile.read(loaded);storageError="";storageLoaded=true;render();resumeSavedAvailability();});}catch(Exception e){runOnUiThread(()->{if(isDestroyed())return;storageError=getString(R.string.settings_unavailable);storageLoaded=true;render();});}});}
    private void restoreCallSelection(JSONObject loaded){
        if(selectionInitialized)return;selectionInitialized=true;if(selectionFromState)return;
        JSONObject pending=loaded.optJSONObject("pending_call");if(!selectedLine.isEmpty()||pending==null)return;
        String line=pending.optString("line"),card=pending.optString("card"),mode=pending.optString("mode");
        if(line.isEmpty()||card.isEmpty()||(!mode.equals("cellular")&&!mode.equals("vowifi")))return;
        selectedLine=line;selectedCard=card;selectedRoute=mode.equals("cellular")?1:0;
    }
    @Override protected void onStop(){captureLoginDraft();persistLoginDraft();closeHistory();if(toneDialog!=null)toneDialog.dismiss();if(service!=null)service.removeListener(update);if(bound){unbindService(binding);bound=false;}service=null;super.onStop();}
    @Override protected void onDestroy(){cancelLogin();draftHandler.removeCallbacks(saveDraft);io.shutdown();super.onDestroy();}
    private int dp(int v){return Math.round(v*getResources().getDisplayMetrics().density);}
    private void build(){
        page=new LinearLayout(this);page.setOrientation(LinearLayout.VERTICAL);page.setBackgroundColor(0xfff5f7fa);
        page.setPadding(dp(12),dp(8),dp(12),dp(4));
        page.setOnApplyWindowInsetsListener((view,insets)->{
            if(Build.VERSION.SDK_INT>=30){android.graphics.Insets bars=insets.getInsets(WindowInsets.Type.systemBars());int bottom=Math.max(bars.bottom,insets.getInsets(WindowInsets.Type.ime()).bottom);page.setPadding(dp(12)+bars.left,dp(4)+bars.top,dp(12)+bars.right,dp(4)+bottom);if(navigation!=null)navigation.setVisibility(insets.isVisible(WindowInsets.Type.ime())?View.GONE:View.VISIBLE);return WindowInsets.CONSUMED;}
            return insets.consumeSystemWindowInsets();
        });
        LinearLayout header=new LinearLayout(this);header.setGravity(Gravity.CENTER_VERTICAL);header.setMinimumHeight(dp(40));
        pageTitle=text("",18);pageTitle.setId(R.id.page_title);pageTitle.setMaxLines(1);pageTitle.setEllipsize(android.text.TextUtils.TruncateAt.END);pageTitle.setTypeface(null,android.graphics.Typeface.BOLD);header.addView(pageTitle,new LinearLayout.LayoutParams(0,-2,1));
        status=text("",12);status.setId(R.id.gateway_status);status.setMaxLines(1);status.setEllipsize(android.text.TextUtils.TruncateAt.END);status.setOnClickListener(v->{if(service!=null)error(service.connection.render(this));});header.addView(status,new LinearLayout.LayoutParams(dp(108),-2));page.addView(header);
        notice=text("",13);notice.setId(R.id.operation_notice);notice.setMaxLines(4);notice.setEllipsize(android.text.TextUtils.TruncateAt.END);notice.setOnClickListener(v->{if(service!=null)error(service.notice.render(this));});notice.setTextColor(UiLabels.WARNING);notice.setVisibility(View.GONE);page.addView(notice);
        callBox=new LinearLayout(this);callBox.setId(R.id.call_surface);callBox.setOrientation(LinearLayout.VERTICAL);page.addView(callBox);
        contentScroll=new ScrollView(this);contentScroll.setFillViewport(true);renderedTab=-1;content=new LinearLayout(this);content.setOrientation(LinearLayout.VERTICAL);contentScroll.addView(content);page.addView(contentScroll,new LinearLayout.LayoutParams(-1,0,1));
        primaryBar=new LinearLayout(this);primaryBar.setOrientation(LinearLayout.VERTICAL);primaryBar.setVisibility(View.GONE);page.addView(primaryBar);
        navigation=new BottomNavigationView(this);navigation.setId(R.id.bottom_navigation);navigation.setBackgroundColor(0xffffffff);navigation.setElevation(0);navigation.setMinimumHeight(dp(64));
        navigation.setItemHorizontalTranslationEnabled(false);navigation.setItemActiveIndicatorEnabled(false);navigation.setLabelVisibilityMode(NavigationBarView.LABEL_VISIBILITY_LABELED);
        navigation.setLabelMaxLines(2);navigation.setItemPaddingTop(dp(4));navigation.setItemPaddingBottom(dp(4));
        navigation.setItemTextAppearanceActive(R.style.NavLabel);navigation.setItemTextAppearanceInactive(R.style.NavLabel);
        int[] labels={R.string.home,R.string.calls,R.string.messages,R.string.readers,R.string.settings};
        int[] ids={R.id.tab_home,R.id.tab_calls,R.id.tab_messages,R.id.tab_readers,R.id.tab_settings};
        int[] icons={R.drawable.ic_mdd_home,R.drawable.ic_mdd_call,R.drawable.ic_mdd_message,R.drawable.ic_mdd_sim_card,R.drawable.ic_mdd_settings};
        for(int i=0;i<ids.length;i++)navigation.getMenu().add(0,ids[i],i,labels[i]).setIcon(icons[i]).setContentDescription(getString(labels[i]));
        navigation.setSelectedItemId(ids[tab]);
        navigation.setOnItemSelectedListener(item->{for(int i=0;i<ids.length;i++)if(item.getItemId()==ids[i]){if(tab!=i){tab=i;focusIncoming="";render();}return true;}return false;});
        page.addView(navigation,new LinearLayout.LayoutParams(-1,-2));setContentView(page);render();
    }
    private TextView text(String value,int size){TextView view=new TextView(this);view.setText(value);view.setTextSize(size);view.setTextColor(0xff172334);view.setPadding(0,dp(4),0,dp(4));return view;}
    private void label(String value){content.addView(text(value,15));}
    private MaterialButton command(String name){
        MaterialButton button=new MaterialButton(this);button.setText(name);button.setAllCaps(false);button.setTextSize(14);button.setMinHeight(dp(48));button.setMinimumWidth(0);button.setMaxLines(3);
        button.setCornerRadius(dp(8));button.setElevation(0);button.setStateListAnimator(null);
        button.setBackgroundTintList(ColorStateList.valueOf(0x00000000));button.setStrokeWidth(dp(1));button.setStrokeColor(ColorStateList.valueOf(0xffc6d5da));
        button.setTextColor(new ColorStateList(new int[][]{new int[]{android.R.attr.state_enabled},new int[]{}},new int[]{0xff244e63,0xff7b898f}));
        button.setIconTint(button.getTextColors());
        return button;
    }
    private Button button(LinearLayout parent,String name,Runnable action){MaterialButton button=command(name);button.setOnClickListener(v->{try{action.run();}catch(Exception e){error(e.getMessage());}});parent.addView(button,parent.getOrientation()==LinearLayout.HORIZONTAL?new LinearLayout.LayoutParams(0,-2,1):new LinearLayout.LayoutParams(-1,-2));return button;}
    private Button button(int name,Runnable action){
        boolean primary=name==R.string.dial||name==R.string.send||name==R.string.connect||name==R.string.hangup;
        MaterialButton b=(MaterialButton)button(primary?primaryBar:content,getString(name),action);
        if(primary){primaryBar.setVisibility(View.VISIBLE);b.setStrokeWidth(0);b.setTextColor(new ColorStateList(new int[][]{new int[]{android.R.attr.state_enabled},new int[]{}},new int[]{0xffffffff,0xff667a80}));b.setIconTint(b.getTextColors());b.setBackgroundTintList(new ColorStateList(new int[][]{new int[]{android.R.attr.state_enabled},new int[]{}},new int[]{0xff0c6b64,0xffdce5e8}));}
        if(name==R.string.dial){b.setId(R.id.call_dial);b.setIconResource(R.drawable.ic_mdd_call);b.setIconGravity(MaterialButton.ICON_GRAVITY_TEXT_START);}
        if(name==R.string.send){b.setId(R.id.message_send);b.setIconResource(R.drawable.ic_mdd_message);b.setIconGravity(MaterialButton.ICON_GRAVITY_TEXT_START);}
        if(name==R.string.hangup){b.setId(R.id.call_hangup);b.setBackgroundTintList(ColorStateList.valueOf(0xffb3261e));}
        if(name==R.string.share)b.setId(R.id.reader_share);if(name==R.string.gateway)b.setId(R.id.gateway_manage);if(name==R.string.resume||name==R.string.pause)b.setId(R.id.availability_toggle);return b;
    }
    private ImageButton tool(int icon,String title,Runnable action){
        ImageButton button=new ImageButton(this);button.setImageResource(icon);button.setImageTintList(ColorStateList.valueOf(0xff37618a));button.setPadding(dp(12),dp(12),dp(12),dp(12));button.setContentDescription(title);button.setTooltipText(title);
        android.util.TypedValue value=new android.util.TypedValue();getTheme().resolveAttribute(android.R.attr.selectableItemBackgroundBorderless,value,true);button.setBackgroundResource(value.resourceId);
        button.setOnClickListener(v->{try{action.run();}catch(Exception e){error(e.getMessage());}});return button;
    }
    private EditText input(int hint,boolean secret){
        TextInputLayout layout=new TextInputLayout(this);layout.setBoxBackgroundMode(TextInputLayout.BOX_BACKGROUND_OUTLINE);layout.setHint(getString(hint));layout.setBoxCornerRadii(dp(8),dp(8),dp(8),dp(8));
        TextInputEditText edit=new TextInputEditText(layout.getContext());edit.setSingleLine(true);edit.setTextSize(16);edit.setInputType(secret?InputType.TYPE_CLASS_TEXT|InputType.TYPE_TEXT_VARIATION_PASSWORD:InputType.TYPE_CLASS_TEXT);edit.setImeOptions(android.view.inputmethod.EditorInfo.IME_FLAG_NO_EXTRACT_UI|android.view.inputmethod.EditorInfo.IME_FLAG_NO_FULLSCREEN);
        if(hint==R.string.server)edit.setId(R.id.gateway_server);if(hint==R.string.fingerprint)edit.setId(R.id.gateway_pin);if(hint==R.string.username)edit.setId(R.id.gateway_username);if(hint==R.string.password)edit.setId(R.id.gateway_password);if(hint==R.string.number)edit.setId(R.id.dial_number);if(hint==R.string.sms_body)edit.setId(R.id.message_body);
        layout.addView(edit,new LinearLayout.LayoutParams(-1,-2));LinearLayout.LayoutParams params=new LinearLayout.LayoutParams(-1,-2);params.bottomMargin=dp(8);content.addView(layout,params);return edit;
    }
    private void error(String message){new AlertDialog.Builder(this).setMessage(message==null?getString(R.string.not_available):message).setPositiveButton(android.R.string.ok,null).show();}
    private void confirm(String message,Runnable action){new AlertDialog.Builder(this).setMessage(message).setPositiveButton(R.string.confirm,(d,w)->{try{action.run();}catch(Exception e){error(e.getMessage());}}).setNegativeButton(R.string.cancel,null).show();}
    private void startAvailability(){if(savedConfig.optString("token").isEmpty()){tab=0;render();return;}startForegroundService(new Intent(this,AgentService.class).setAction(AgentService.START));}
    private void render(){if(content==null)return;captureDraft();captureLoginDraft();content.removeAllViews();primaryBar.removeAllViews();primaryBar.setVisibility(View.GONE);callSurfaceKey="";callBox.removeAllViews();callAudioDetails=null;messageList=null;messageOperations=null;messageOperationView="";readerSummary=null;readerItems=null;usbPermissions=null;usbPermissionState="";readerItemsState="";sharingButton=null;capability=null;directoryStatus=null;homeConnection=homeAvailability=homeReaders=homeLines=homeActivity=null;homeEvents=null;homeEventsRevision=-1;number=message=null;lineChoice=null;routeChoice=null;server=pin=username=password=null;rememberLogin=null;draftStatus=null;
        homeUsbState=readerUsbState=null;
        if(renderedTab!=tab){renderedTab=tab;contentScroll.scrollTo(0,0);}
        int[] titles={R.string.home,R.string.calls,R.string.messages,R.string.readers,R.string.settings};int[] tabs={R.id.tab_home,R.id.tab_calls,R.id.tab_messages,R.id.tab_readers,R.id.tab_settings};pageTitle.setText("MDD · "+getString(titles[tab]));pageTitle.setContentDescription("page:"+new String[]{"home","calls","messages","readers","settings"}[tab]);navigation.getMenu().findItem(tabs[tab]).setChecked(true);
        if(!storageLoaded){label(getString(R.string.reading_settings));return;}if(!storageError.isEmpty()){label(storageError);button(content,getString(R.string.storage_retry),this::reloadStorage);if(service!=null&&service.call!=null)button(content,getString(R.string.end_known_call),service::hangup);return;}
        JSONObject config=savedConfig;if(config.optString("token").isEmpty()&&tab==0){setup(config);updateState();return;}
        if(config.optString("token").isEmpty())button(content,getString(R.string.connect),()->{tab=0;render();});
        switch(tab){case 1:callPage();break;case 2:messagePage();break;case 3:readerPage();break;case 4:settingsPage(config);break;default:homePage(config);}updateState();}
    private void setup(JSONObject config){
        if(loginProfile==null)loginProfile=LoginProfile.read(config);
        server=input(R.string.server,false);server.setInputType(InputType.TYPE_CLASS_TEXT|InputType.TYPE_TEXT_VARIATION_URI);server.setText(loginProfile.address);
        username=input(R.string.username,false);username.setText(loginProfile.username);
        password=input(R.string.password,true);password.setSaveEnabled(false);password.setSaveFromParentEnabled(false);password.setText(loginProfile.password);
        View passwordBox=content.getChildAt(content.getChildCount()-1);((TextInputLayout)passwordBox).setEndIconMode(TextInputLayout.END_ICON_PASSWORD_TOGGLE);
        rememberLogin=new com.google.android.material.checkbox.MaterialCheckBox(this);rememberLogin.setId(R.id.login_remember);rememberLogin.setText(R.string.remember_login);rememberLogin.setChecked(loginProfile.remember);content.addView(rememberLogin);
        draftStatus=text("",12);draftStatus.setId(R.id.login_saved);content.addView(draftStatus);
        Button certificate=button(content,getString(R.string.certificate_options),()->{captureLoginDraft();certificateExpanded=!certificateExpanded;render();});certificate.setId(R.id.certificate_options);
        pin=input(R.string.fingerprint,false);pin.setText(loginProfile.manualPin);content.getChildAt(content.getChildCount()-1).setVisibility(certificateExpanded?View.VISIBLE:View.GONE);
        watchLogin(server,true);watchLogin(username,true);watchLogin(password,false);watchLogin(pin,false);
        rememberLogin.setOnCheckedChangeListener((button,checked)->{captureLoginDraft();loginVersion++;persistLoginDraft();});
        Button connect=button(R.string.connect,()->login());connect.setId(R.id.connect_gateway);setLoginBusy(loginBusy);
        button(R.string.scan,()->new IntentIntegrator(this).setDesiredBarcodeFormats(IntentIntegrator.QR_CODE).setBeepEnabled(false).setPrompt(getString(R.string.setup_title)).initiateScan());
        button(R.string.import_setup,()->{EditText v=new EditText(this);v.setHint("{\"type\":\"mdd-agent-setup\",…}");new AlertDialog.Builder(this).setTitle(R.string.import_setup).setView(v).setPositiveButton(R.string.confirm,(d,w)->importSetup(v.getText().toString())).setNegativeButton(R.string.cancel,null).show();});
        button(R.string.help,()->error(getString(R.string.help_text)));}
    private void captureLoginDraft(){if(loginProfile==null||server==null)return;loginProfile.address=server.getText().toString();loginProfile.username=username.getText().toString();loginProfile.password=password.getText().toString();loginProfile.manualPin=pin.getText().toString();loginProfile.remember=rememberLogin.isChecked();}
    private void watchLogin(EditText field,boolean identity){field.addTextChangedListener(new android.text.TextWatcher(){public void beforeTextChanged(CharSequence s,int start,int count,int after){}public void onTextChanged(CharSequence s,int start,int before,int count){if(identity){password.setText("");if(field==server){pin.setText("");loginProfile.enrollmentOrigin=loginProfile.agentID=loginProfile.agentToken="";}}captureLoginDraft();loginVersion++;draftHandler.removeCallbacks(saveDraft);draftHandler.postDelayed(saveDraft,350);}public void afterTextChanged(android.text.Editable value){}});}
    private void persistLoginDraft(){persistLoginDraft(null);}
    private void persistLoginDraft(Runnable success){draftHandler.removeCallbacks(saveDraft);if(loginProfile==null||!storageError.isEmpty())return;final JSONObject draft=loginProfile.persisted();final long version=loginVersion;
        ConfigStore.intent(()->{try{new ConfigStore(this).update(current->current.put("login_profile",draft));runOnUiThread(()->{if(isDestroyed()||version!=loginVersion)return;if(draftStatus!=null)draftStatus.setText(draft.optBoolean("remember")?R.string.login_saved:R.string.password_not_saved);if(success!=null)success.run();});}catch(Exception failure){runOnUiThread(()->{if(isDestroyed()||version!=loginVersion)return;if(draftStatus!=null)draftStatus.setText(R.string.login_save_failed);else error(getString(R.string.login_save_failed));});}});
    }
    private void setLoginBusy(boolean busy){loginBusy=busy;for(EditText field:new EditText[]{server,username,password,pin})if(field!=null)field.setEnabled(!busy);if(rememberLogin!=null)rememberLogin.setEnabled(!busy);Button connect=findViewById(R.id.connect_gateway);if(connect!=null){connect.setEnabled(!busy);connect.setText(busy?R.string.connecting:R.string.connect);}}
    private boolean currentLogin(long version){return !isDestroyed()&&!isFinishing()&&loginBusy&&loginVersion==version;}
    private void cancelLogin(){loginVersion++;setLoginBusy(false);GatewayApi request=loginRequest;loginRequest=null;if(request!=null)request.close();}
    private void loginFailed(long version,Exception failure){runOnUiThread(()->{if(!currentLogin(version))return;setLoginBusy(false);error(getString(R.string.login_failed)+" "+RemoteCall.safe(failure));});}
    private void login(){
        captureLoginDraft();final Endpoint endpoint;try{endpoint=loginProfile.endpoint();}catch(Exception failure){error(failure.getMessage());return;}
        final String user=loginProfile.username.trim(),pass=loginProfile.password;if(user.isEmpty()||pass.isEmpty()){error(getString(R.string.login_credentials_required));return;}
        final long version=++loginVersion;final JSONObject draft=loginProfile.persisted();setLoginBusy(true);
        ConfigStore.intent(()->{try{JSONObject current=new ConfigStore(this).update(state->state.put("login_profile",draft));String previous=LoginProfile.pin(current,endpoint.origin);
            io.execute(()->{try{CertificateProbe.Presented presented=CertificateProbe.inspect(endpoint);runOnUiThread(()->{
                if(!currentLogin(version))return;
                if(!endpoint.fingerprint.isEmpty()&&!endpoint.fingerprint.equals(presented.fingerprint)){setLoginBusy(false);error(getString(R.string.certificate_pin_mismatch));return;}
                if(presented.fingerprint.equals(previous)||previous.isEmpty()&&!endpoint.fingerprint.isEmpty()){authenticate(version,endpoint,user,pass,previous,presented);return;}
                String details=endpoint.origin+"\n\n"+(previous.isEmpty()?getString(R.string.certificate_first_warning):getString(R.string.certificate_changed_warning)+"\n\n"+getString(R.string.certificate_previous)+"\n"+previous)+"\n\nSHA-256\n"+presented.fingerprint+"\n\n"+presented.subject+"\n"+java.text.DateFormat.getDateInstance().format(new Date(presented.notAfter));
                AlertDialog dialog=new AlertDialog.Builder(this).setTitle(previous.isEmpty()?R.string.certificate_first:R.string.certificate_changed).setMessage(details).setNegativeButton(R.string.cancel,(d,w)->{loginVersion++;setLoginBusy(false);}).setPositiveButton(R.string.certificate_trust,(d,w)->{if(currentLogin(version))authenticate(version,endpoint,user,pass,previous,presented);}).create();dialog.setOnCancelListener(d->{loginVersion++;setLoginBusy(false);});dialog.show();
            });}catch(Exception failure){loginFailed(version,failure);}});
        }catch(Exception failure){loginFailed(version,failure);}});
    }
    private void authenticate(long version,Endpoint original,String user,String pass,String previous,CertificateProbe.Presented certificate){
        if(!currentLogin(version))return;
        final String enrollmentOrigin=loginProfile.enrollmentOrigin,enrolledID=loginProfile.agentID,enrolledToken=loginProfile.agentToken;
        ConfigStore.intent(()->{try{
            if(!currentLogin(version))return;new ConfigStore(this).update(current->{if(!currentLogin(version))throw new IllegalStateException("Login target changed");LoginProfile.acceptPin(current,original.origin,previous,certificate.fingerprint);});
            io.execute(()->{GatewayApi candidate=null;try{
                if(!currentLogin(version))return;Endpoint endpoint=new Endpoint(original.origin,certificate.fingerprint);candidate=new GatewayApi(endpoint,"","");loginRequest=candidate;if(!currentLogin(version))return;candidate.login(user,pass);if(!currentLogin(version))return;
                final String token=candidate.token,csrf=candidate.csrf,scope=candidate.authenticatedScope(),accountName=candidate.authenticatedName();
                ConfigStore.intent(()->{try{if(!currentLogin(version))return;JSONObject cfg=new ConfigStore(this).update(current->{
                    if(!currentLogin(version))throw new IllegalStateException("Login target changed");
                    String prior=current.optString("server");if(!prior.isEmpty())prior=new Endpoint(prior,"").origin;
                    if(!prior.equals(endpoint.origin)){current.remove("agent_token");current.remove("agent_id");current.remove("share");}
                    if(enrollmentOrigin.equals(endpoint.origin)&&!enrolledID.isEmpty()&&!enrolledToken.isEmpty())current.put("agent_id",enrolledID).put("agent_token",enrolledToken);
                    MessageJournal.adoptConfirmedTrustChange(current,endpoint.origin,endpoint.fingerprint,accountName);
                    current.put("server",endpoint.origin).put("pin",endpoint.fingerprint).put("username",user).put("token",token).put("csrf",csrf).put("account_scope",scope).put("account_user",accountName);
                });runOnUiThread(()->{if(!currentLogin(version))return;setLoginBusy(false);requestNotification();savedConfig=cfg;startAvailability();render();});}catch(Exception failure){loginFailed(version,failure);}});
            }catch(Exception failure){loginFailed(version,failure);}finally{if(loginRequest==candidate)loginRequest=null;if(candidate!=null)candidate.close();}});
        }catch(Exception failure){loginFailed(version,failure);}});
    }
    private void importSetup(String data){try{Setup s=new Setup(data);confirm(getString(R.string.setup_confirm,s.endpoint.origin,s.endpoint.fingerprint),()->{captureLoginDraft();loginVersion++;setLoginBusy(false);loginProfile.address=s.endpoint.origin;loginProfile.password="";loginProfile.manualPin=s.endpoint.fingerprint;loginProfile.enrollmentOrigin=s.endpoint.origin;loginProfile.agentID=s.agentID;loginProfile.agentToken=s.agentToken;server=pin=username=password=null;persistLoginDraft();render();});}catch(Exception e){error(e.getMessage());}}
    @Override protected void onActivityResult(int code,int result,Intent data){IntentResult scan=IntentIntegrator.parseActivityResult(code,result,data);if(scan!=null){if(scan.getContents()!=null)importSetup(scan.getContents());}else super.onActivityResult(code,result,data);}
    private void requestNotification(){if(Build.VERSION.SDK_INT>=33&&checkSelfPermission(Manifest.permission.POST_NOTIFICATIONS)!=PackageManager.PERMISSION_GRANTED)requestPermissions(new String[]{Manifest.permission.POST_NOTIFICATIONS},21);}
    private void homePage(JSONObject c){
        section(R.string.home_gateway_section);label(c.optString("server"));homeConnection=text("",15);homeConnection.setId(R.id.home_connection_state);content.addView(homeConnection);
        homeAvailability=text("",13);homeAvailability.setId(R.id.home_availability_state);content.addView(homeAvailability);
        Button availability=button(service!=null&&service.available()?R.string.pause:R.string.resume,()->{if(service!=null&&service.available())service.pause();else{requestNotification();startAvailability();}});
        availability.setId(R.id.home_availability_toggle);availability.setEnabled(service!=null&&!service.intentSaving());
        section(R.string.home_readers_section);homeReaders=text("",14);homeReaders.setId(R.id.home_reader_state);content.addView(homeReaders);
        homeUsbState=text("",14);homeUsbState.setId(R.id.home_usb_state);content.addView(homeUsbState);
        button(content,getString(R.string.open_readers),()->openTab(3)).setId(R.id.home_open_readers);
        section(R.string.home_lines_section);homeLines=text("",14);homeLines.setId(R.id.home_line_state);content.addView(homeLines);
        LinearLayout lineActions=new LinearLayout(this);lineActions.setOrientation(LinearLayout.HORIZONTAL);content.addView(lineActions);
        button(lineActions,getString(R.string.open_calls),()->openTab(1)).setId(R.id.home_open_calls);
        button(lineActions,getString(R.string.open_messages),()->openTab(2)).setId(R.id.home_open_messages);
        section(R.string.home_activity_section);homeActivity=text("",14);homeActivity.setId(R.id.home_activity_state);content.addView(homeActivity);
        section(R.string.home_recent_events_section);homeEvents=new LinearLayout(this);homeEvents.setId(R.id.home_recent_events);homeEvents.setOrientation(LinearLayout.VERTICAL);content.addView(homeEvents);
        button(content,getString(R.string.diagnostics),this::diagnostics).setId(R.id.home_diagnostics);
        button(content,getString(R.string.open_settings),()->openTab(4)).setId(R.id.home_settings);
        button(R.string.help,()->error(getString(R.string.help_text)));updateHomePage();
    }
    private void openTab(int target){int[] ids={R.id.tab_home,R.id.tab_calls,R.id.tab_messages,R.id.tab_readers,R.id.tab_settings};if(target>=0&&target<ids.length&&navigation!=null)navigation.setSelectedItemId(ids[target]);}
    private void updateHomePage(){
        if(homeConnection==null)return;
        updateUsbReaderStatus(homeUsbState);
        if(service==null){homeConnection.setText(R.string.local_service_connecting);homeAvailability.setText(R.string.not_available);homeReaders.setText(R.string.not_available);homeLines.setText(R.string.not_available);homeActivity.setText(R.string.not_available);updateHomeEvents();return;}
        homeConnection.setText(getString(R.string.home_connection_status,service.connection.render(this)));
        homeConnection.setTextColor(UiLabels.statusColor(service.connection));
        homeAvailability.setText(getString(R.string.home_availability_status,getString(service.available()?R.string.availability_on:R.string.paused)));
        homeAvailability.setTextColor(service.available()?UiLabels.OK:UiLabels.NEUTRAL);
        int attached=attachedCcidReaders().size(),pending=pendingReaderCount();
        String readerState=!service.available()?(service.sharing()?getString(R.string.sharing_paused):getString(R.string.sharing_off)):
            service.sharing()?(service.readerLinkOnline?service.readerStatus.render(this):service.readerConnection.render(this)):getString(R.string.sharing_off);
        homeReaders.setText(getString(R.string.home_reader_status,readerState,attached,pending));
        homeReaders.setTextColor(service.sharing()&&!service.readerLinkOnline?UiLabels.statusColor(service.readerConnection):pending>0?UiLabels.WARNING:UiLabels.statusColor(service.readerStatus));
        JSONArray lines=Json.array(service.snapshot,"lines");int enabled=0;
        for(int i=0;i<lines.length();i++){JSONObject line=lines.optJSONObject(i);if(line!=null&&line.optBoolean("enabled"))enabled++;}
        homeLines.setText(getString(R.string.home_line_status,lines.length(),enabled));
        homeLines.setTextColor(!service.online()||lines.length()==0?UiLabels.WARNING:UiLabels.OK);
        if(!service.online())homeLines.append("\n"+getString(service.connection.resource==R.string.link_upgrade?R.string.core_mobile_endpoint_required:R.string.directory_offline));
        else if(lines.length()==0)homeLines.append("\n"+getString(R.string.directory_empty));
        if(service.snapshot.optBoolean("incomplete"))homeLines.append("\n"+getString(R.string.directory_partial));
        JSONObject privateState=service.config();JSONArray operations=Json.array(privateState,"message_operations");int unresolved=0;
        for(int i=0;i<operations.length();i++){JSONObject row=operations.optJSONObject(i);if(row!=null&&!MessageJournal.resolved(row))unresolved++;}
        String call=service.call==null?getString(R.string.call_recovery_none):service.call.state.render(this);
        homeActivity.setText(getString(R.string.home_activity_status,Json.array(service.snapshot,"messages").length(),unresolved,call));
        homeActivity.setTextColor(unresolved>0?UiLabels.WARNING:service.call==null?UiLabels.NEUTRAL:UiLabels.statusColor(service.call.state));
        renderReaderBadge(pending);
        updateHomeEvents();
    }
    private void updateHomeEvents(){
        if(homeEvents==null)return;
        long revision=service==null?0:service.activityLog.revision();if(revision==homeEventsRevision)return;homeEventsRevision=revision;homeEvents.removeAllViews();
        List<AgentActivityLog.Entry> events=service==null?Collections.emptyList():service.activityLog.recent();
        if(events.isEmpty()){homeEvents.addView(text(getString(R.string.home_recent_events_empty),13));return;}
        java.text.DateFormat time=android.text.format.DateFormat.getTimeFormat(this);
        for(AgentActivityLog.Entry event:events){
            LinearLayout row=new LinearLayout(this);row.setGravity(Gravity.TOP);TextView at=text(time.format(new java.util.Date(event.timestamp)),12);at.setTextColor(0xff667a80);row.addView(at,new LinearLayout.LayoutParams(dp(64),-2));
            TextView detail=text(getString(event.message),13);detail.setTextColor(UiLabels.statusColor(UiText.of(event.message)));row.addView(detail,new LinearLayout.LayoutParams(0,-2,1));homeEvents.addView(row);
        }
    }
    private ArrayList<UsbDevice> attachedCcidReaders(){
        UsbManager manager=getSystemService(UsbManager.class);ArrayList<UsbDevice> result=new ArrayList<>();if(manager==null)return result;
        for(UsbDevice device:manager.getDeviceList().values()){boolean ccid=false;for(int i=0;i<device.getInterfaceCount();i++)ccid|=device.getInterface(i).getInterfaceClass()==11;if(ccid)result.add(device);}
        result.sort(Comparator.comparingInt(UsbDevice::getDeviceId));return result;
    }
    private static String usbReaderName(UsbDevice device){return "USB-"+device.getVendorId()+"-"+device.getProductId()+"-"+device.getDeviceId()+"-slot0";}
    private int pendingReaderCount(){
        UsbManager manager=getSystemService(UsbManager.class);if(manager==null)return 0;ArrayList<String> attached=new ArrayList<>(),permitted=new ArrayList<>(),scanned=new ArrayList<>();
        for(UsbDevice device:attachedCcidReaders()){String name=usbReaderName(device);attached.add(name);if(manager.hasPermission(device))permitted.add(name);}
        if(service!=null){JSONArray rows=Json.array(service.readers(),"readers");for(int i=0;i<rows.length();i++){JSONObject row=rows.optJSONObject(i);if(row!=null)scanned.add(row.optString("reader_name"));}}
        return ReaderAttention.pending(attached,permitted,scanned,service!=null&&service.sharing(),service!=null&&service.readerLinkOnline);
    }
    private void updateReaderBadge(){renderReaderBadge(pendingReaderCount());}
    void renderReaderBadge(int pending){
        if(navigation==null)return;android.view.MenuItem item=navigation.getMenu().findItem(R.id.tab_readers);
        if(pending<=0){if(navigation.getBadge(R.id.tab_readers)!=null)navigation.removeBadge(R.id.tab_readers);item.setContentDescription(getString(R.string.readers));return;}
        BadgeDrawable badge=navigation.getOrCreateBadge(R.id.tab_readers);badge.setBackgroundColor(0xffb3261e);badge.setBadgeTextColor(0xffffffff);badge.setNumber(Math.min(pending,99));badge.setVisible(true);
        item.setContentDescription(getString(R.string.reader_attention_accessibility,pending));
    }
    private String lineLabel(JSONObject line){return UiLabels.line(this,line);}
    private JSONArray lines(){return service==null?new JSONArray():Json.array(service.snapshot,"lines");}
    private void selectors(){
        if(service!=null)service.refreshLineNumbers(true);
        selectionSnapshot=new JSONArray();LinearLayout row=new LinearLayout(this);row.setGravity(Gravity.CENTER_VERTICAL);
        lineChoice=new Spinner(this);lineChoice.setDropDownWidth(ViewGroup.LayoutParams.MATCH_PARENT);lineChoice.setId(R.id.line_selector);row.addView(lineChoice,new LinearLayout.LayoutParams(0,-2,1));renderedLineLabels.clear();
        row.addView(tool(R.drawable.ic_mdd_search,getString(R.string.search_lines),this::directoryDialog),new LinearLayout.LayoutParams(dp(48),dp(48)));content.addView(row);
        routeChoice=new MaterialButtonToggleGroup(this);routeChoice.setId(R.id.route_selector);routeChoice.setSingleSelection(true);routeChoice.setSelectionRequired(true);
        for(int i=0;i<2;i++){MaterialButton b=command(i==0?"VoWiFi":getString(R.string.cellular));b.setId(i==0?R.id.route_vowifi:R.id.route_cellular);b.setCheckable(true);
            b.setBackgroundTintList(new ColorStateList(new int[][]{new int[]{android.R.attr.state_checked},new int[]{}},new int[]{0xffdfefed,0xffffffff}));
            routeChoice.addView(b,new LinearLayout.LayoutParams(0,-2,1));}
        routeChoice.check(selectedRoute==1?R.id.route_cellular:R.id.route_vowifi);content.addView(routeChoice);
        capability=text("",12);content.addView(capability);
        directoryStatus=text("",13);directoryStatus.setId(R.id.directory_status);directoryStatus.setVisibility(View.GONE);content.addView(directoryStatus);
        lineChoice.setOnItemSelectedListener(new AdapterView.OnItemSelectedListener(){public void onItemSelected(AdapterView<?> parent,View view,int position,long id){updateCapability();}public void onNothingSelected(AdapterView<?> parent){updateCapability();}});
        routeChoice.addOnButtonCheckedListener((group,id,checked)->{if(checked){selectedRoute=id==R.id.route_cellular?1:0;updateCapability();}});
        updateSelection();
    }
    private JSONObject selected(){int index=lineChoice==null?-1:lineChoice.getSelectedItemPosition();JSONObject l=selectionSnapshot.optJSONObject(index);if(l==null||!l.optBoolean("enabled"))throw new IllegalArgumentException(getString(R.string.line_unavailable));selectedLine=l.optString("id");try{return new JSONObject(l.toString());}catch(Exception e){throw new IllegalArgumentException(e);}}
    private String route(){return routeChoice!=null&&routeChoice.getCheckedButtonId()==R.id.route_cellular?"cellular":"vowifi";}
    private void callPage(){
        button(content,getString(R.string.call_history),this::callHistoryDialog);
        if(service!=null&&service.call!=null){
            RemoteCall current=service.call;button(R.string.hangup,current::hangup);
            if(current.audio!=null&&!current.audio.closed){
                LinearLayout media=new LinearLayout(this);
                MaterialButton mute=(MaterialButton)button(media,getString(R.string.mute),()->{if(current.audio!=null&&!current.audio.closed)current.audio.mute();});mute.setId(R.id.call_mute);mute.setCheckable(true);
                MaterialButton speaker=(MaterialButton)button(media,getString(R.string.speaker),()->{if(current.audio!=null&&!current.audio.closed)current.audio.speaker();});speaker.setId(R.id.call_speaker);speaker.setCheckable(true);content.addView(media);
                styleAudioToggle(mute,R.drawable.ic_mdd_mic);styleAudioToggle(speaker,R.drawable.ic_mdd_volume_up);
                MaterialButton tones=(MaterialButton)button(media,getString(R.string.call_keypad),()->callKeypad(current));tones.setId(R.id.call_dtmf);styleAudioToggle(tones,R.drawable.ic_mdd_dialpad);tones.setContentDescription(getString(R.string.call_keypad));tones.setTooltipText(getString(R.string.call_keypad));
            }
            callAudioDetails=text("",13);callAudioDetails.setId(R.id.call_audio_state);content.addView(callAudioDetails);
            TextView identity=text(callLineIdentity(current)+"\n"+current.plan.number+" · "+UiLabels.transport(this,current.plan.mode),15);identity.setId(R.id.call_line_identity);content.addView(identity);
            button(content,getString(R.string.check_call),current::reconcile).setId(R.id.call_reconcile);
            button(content,getString(R.string.check_gateway),()->startActivity(new Intent(Intent.ACTION_VIEW,Uri.parse(savedConfig.optString("server")))));
            return;
        }
        selectors();LinearLayout entry=new LinearLayout(this);entry.setGravity(Gravity.CENTER_VERTICAL);
        ImageButton plus=tool(R.drawable.ic_mdd_add,getString(R.string.international_plus),()->{if(number!=null)number.getText().insert(Math.max(0,number.getSelectionStart()),"+");});plus.setId(R.id.dial_plus);entry.addView(plus,new LinearLayout.LayoutParams(dp(48),dp(48)));
        number=new EditText(this);number.setId(R.id.dial_number);number.setHint(R.string.number);number.setSingleLine(true);number.setInputType(InputType.TYPE_CLASS_PHONE);number.setImeOptions(android.view.inputmethod.EditorInfo.IME_FLAG_NO_EXTRACT_UI|android.view.inputmethod.EditorInfo.IME_FLAG_NO_FULLSCREEN);number.setTextSize(18);number.setMinHeight(dp(52));number.setText(draftNumber);entry.addView(number,new LinearLayout.LayoutParams(0,-2,1));
        ImageButton erase=tool(R.drawable.ic_mdd_backspace,getString(R.string.erase_digit),()->{int end=number.getSelectionEnd();if(end>0)number.getText().delete(end-1,end);});erase.setId(R.id.dial_backspace);entry.addView(erase,new LinearLayout.LayoutParams(dp(48),dp(48)));content.addView(entry);
        dialPad();button(R.string.dial,()->{JSONObject l=selected();String mode=route(),target=number.getText().toString();confirm(getString(R.string.call_confirm)+"\n\n"+lineLabel(l)+" · "+UiLabels.transport(this,mode)+"\n"+target,()->startCall(l,mode,target,null));});
        button(R.string.device_dial,()->{try{startActivity(new Intent(Intent.ACTION_DIAL,Uri.fromParts("tel",CallPlan.dialTarget(number.getText().toString()),null)));}catch(Exception e){error(getString(R.string.system_dialer_unavailable));}});
        button(content,getString(R.string.all_incoming),this::incomingDialog).setId(R.id.all_incoming);
    }
    private void callHistoryDialog(){
        if(service==null||!service.online()){error(getString(R.string.connect_first));return;}
        closeHistory();AgentService owner=service;long epoch=owner.accountEpoch();
        LinearLayout rows=new LinearLayout(this);rows.setOrientation(LinearLayout.VERTICAL);rows.setPadding(dp(16),dp(8),dp(16),dp(8));
        TextView loading=text(getString(R.string.reading_history),14);rows.addView(loading);
        ScrollView scroll=new ScrollView(this);scroll.addView(rows);
        AlertDialog dialog=new AlertDialog.Builder(this).setTitle(R.string.call_history).setView(scroll).setNegativeButton(R.string.close,null).create();
        activeHistoryDialog=dialog;historyAccountEpoch=epoch;dialog.show();
        owner.callHistory(epoch,page->{
            if(!dialog.isShowing()||service!=owner||owner.accountEpoch()!=epoch)return;
            rows.removeAllViews();JSONArray calls=Json.array(page,"calls");
            if(calls.length()==0)rows.addView(text(getString(R.string.no_call_history),14));
            for(int i=0;i<calls.length();i++){
                JSONObject call=calls.optJSONObject(i);if(call==null)continue;
                LinearLayout row=new LinearLayout(this);row.setOrientation(LinearLayout.VERTICAL);row.setPadding(0,dp(8),0,dp(12));
                String direction=call.optString("direction"),state=call.optString("status"),peer=call.optString("peer");
                String label=getString(direction.equals("in")?R.string.call_direction_in:direction.equals("out")?R.string.call_direction_out:R.string.unknown);
                TextView heading=text(label+" · "+(peer.isEmpty()?getString(R.string.number_unavailable):peer),16);heading.setTextIsSelectable(true);row.addView(heading);
                TextView outcome=text(UiLabels.callState(this,state)+" · "+UiLabels.transport(this,call.optString("transport")),14);outcome.setTextColor(UiLabels.callColor(state));row.addView(outcome);
                JSONObject line=owner.messageLine(call.optString("line_id"));
                String identity=line.optString("card_id").isEmpty()?getString(R.string.call_history_line_unavailable,call.optString("line_id")):getString(R.string.message_line,lineLabel(line));
                TextView ownLine=text(identity,13);ownLine.setTextIsSelectable(true);row.addView(ownLine);
                String at=call.optString("started_at");
                try{at=android.text.format.DateUtils.formatDateTime(this,java.time.Instant.parse(at).toEpochMilli(),android.text.format.DateUtils.FORMAT_SHOW_DATE|android.text.format.DateUtils.FORMAT_SHOW_TIME|android.text.format.DateUtils.FORMAT_SHOW_YEAR);}catch(java.time.format.DateTimeParseException ignored){}
                TextView time=text(at,12);time.setTextColor(UiLabels.NEUTRAL);row.addView(time);rows.addView(row);
            }
        },failure->{if(dialog.isShowing()){loading.setText(failure);loading.setTextColor(UiLabels.ERROR);}});
    }
    private void dialPad(){
        GridLayout grid=new GridLayout(this);grid.setColumnCount(3);
        for(String digit:new String[]{"1","2","3","4","5","6","7","8","9","*","0","#"}){
            MaterialButton key=command(digit);key.setTextSize(22);key.setContentDescription(digit);key.setStrokeWidth(0);key.setBackgroundTintList(ColorStateList.valueOf(0xffeaf0f3));
            key.setPadding(dp(8),dp(4),dp(8),dp(4));key.setInsetTop(dp(2));key.setInsetBottom(dp(2));
            android.graphics.Paint.FontMetrics metrics=key.getPaint().getFontMetrics();
            GridLayout.LayoutParams lp=new GridLayout.LayoutParams();lp.width=0;lp.height=Math.max(dp(50),(int)Math.ceil(metrics.bottom-metrics.top)+dp(20));lp.columnSpec=GridLayout.spec(GridLayout.UNDEFINED,1f);lp.setMargins(dp(2),0,dp(2),0);key.setLayoutParams(lp);
            key.setOnClickListener(v->{if(number!=null)number.getText().insert(Math.max(0,number.getSelectionStart()),digit);});grid.addView(key);
        }content.addView(grid);
    }
    private void callKeypad(RemoteCall owner){
        if(toneDialog!=null)toneDialog.dismiss();
        LinearLayout box=new LinearLayout(this);box.setOrientation(LinearLayout.VERTICAL);box.setPadding(dp(12),dp(8),dp(12),dp(8));
        toneStatus=text("",14);toneStatus.setId(R.id.dtmf_signal);toneStatus.setAccessibilityLiveRegion(View.ACCESSIBILITY_LIVE_REGION_POLITE);box.addView(toneStatus);
        GridLayout keys=new GridLayout(this);keys.setColumnCount(3);keys.setPadding(dp(12),dp(8),dp(12),dp(8));
        box.addView(keys);toneKeys=keys;toneOwner=owner;
        AlertDialog dialog=new AlertDialog.Builder(this).setTitle(R.string.call_keypad).setView(box).setNegativeButton(R.string.close,null).create();toneDialog=dialog;
        for(String digit:new String[]{"1","2","3","4","5","6","7","8","9","*","0","#"}){
            MaterialButton key=command(digit);key.setTextSize(22);key.setContentDescription(getString(R.string.call_send_tone,digit));
            GridLayout.LayoutParams size=new GridLayout.LayoutParams();size.width=0;size.height=dp(56);size.columnSpec=GridLayout.spec(GridLayout.UNDEFINED,1f);keys.addView(key,size);
            key.setOnClickListener(v->{owner.dtmf(digit);updateToneDialog();});
        }
        dialog.setOnDismissListener(d->{if(toneDialog==dialog){toneDialog=null;toneOwner=null;toneStatus=null;toneKeys=null;}});
        dialog.show();updateToneDialog();
    }
    private void updateToneDialog(){
        if(toneDialog==null||toneOwner==null)return;
        boolean available=service!=null&&service.call==toneOwner&&toneOwner.canSendTone();
        UiText state=toneOwner.toneState;
        if(!available&&state.empty())state=UiText.of(R.string.dtmf_unavailable);
        toneStatus.setText(state.render(this));toneStatus.setTextColor(UiLabels.statusColor(state));
        toneStatus.setVisibility(state.empty()?View.GONE:View.VISIBLE);
        for(int i=0;i<toneKeys.getChildCount();i++)toneKeys.getChildAt(i).setEnabled(available&&!toneOwner.toneBusy());
    }
    private void startCall(JSONObject l,String mode,String number,JSONObject incoming){if(checkSelfPermission(Manifest.permission.RECORD_AUDIO)!=PackageManager.PERMISSION_GRANTED){requestPermissions(new String[]{Manifest.permission.RECORD_AUDIO},22);error(getString(R.string.microphone_permission_needed));return;}try{if(service==null)throw new IllegalStateException(getString(R.string.connect_first));service.begin(new CallPlan(l,mode,number,incoming));}catch(Exception e){error(e.getMessage());}}
    private void messagePage(){selectors();number=input(R.string.number,false);number.setInputType(InputType.TYPE_CLASS_PHONE);number.setText(draftNumber);message=input(R.string.sms_body,false);message.setSingleLine(false);message.setMinLines(3);message.setText(draftMessage);button(R.string.send,()->{JSONObject l=selected();String mode=route(),target=number.getText().toString(),body=message.getText().toString();confirm(getString(R.string.sms_confirm)+"\n\n"+lineLabel(l)+" · "+UiLabels.transport(this,mode)+"\n"+target,()->{try{if(service==null)throw new IllegalStateException(getString(R.string.connect_first));service.sendSMS(l,mode,target,body);}catch(Exception e){error(e.getMessage());}});});
        button(content,getString(R.string.all_conversations),this::conversationDialog).setId(R.id.message_conversations);
        button(content,getString(R.string.line_history),this::historyDialog).setId(R.id.message_history);button(content,getString(R.string.sync_messages),()->{if(service!=null)service.syncMessages();}).setId(R.id.message_sync);
        messageOperations=new LinearLayout(this);messageOperations.setId(R.id.message_operations);messageOperations.setOrientation(LinearLayout.VERTICAL);content.addView(messageOperations);updateMessageOperations();
        label(getString(R.string.recent_messages));messageList=new LinearLayout(this);messageList.setOrientation(LinearLayout.VERTICAL);content.addView(messageList);updateMessages();}
    private void updateMessageOperations(){
        if(messageOperations==null||service==null)return;View sync=findViewById(R.id.message_sync);if(sync!=null)sync.setEnabled(service.canQueryMessages());JSONObject privateState=service.config();String scope=privateState.optString("account_scope");
        if(privateState.optString("token").isEmpty()||!privateState.optString("server").equals(savedConfig.optString("server"))||!savedConfig.optString("account_scope").isEmpty()&&!scope.equals(savedConfig.optString("account_scope"))){messageOperations.removeAllViews();messageOperationView="";return;}
        JSONArray operations=Json.array(privateState,"message_operations");StringBuilder version=new StringBuilder(scope).append(operations).append(service.canQueryMessages()).append(Json.array(service.snapshot,"messages"));for(int i=0;i<operations.length();i++){JSONObject row=operations.optJSONObject(i);if(row!=null)version.append(service.messageChecking(scope,row.optString("operation_id"))).append(lineLabel(service.messageLine(row.optString("line_id"))));}
        boolean notifications=getSystemService(NotificationManager.class).areNotificationsEnabled();version.append(notifications).append(privateState.optJSONObject("last_sms"));if(version.toString().equals(messageOperationView))return;messageOperationView=version.toString();messageOperations.removeAllViews();int other=privateState.optJSONObject("last_sms")==null?0:1;
        for(int i=operations.length()-1;i>=0;i--){JSONObject record=operations.optJSONObject(i);if(record==null)continue;if(!scope.equals(record.optString("scope"))){if(!MessageJournal.resolved(record))other++;continue;}
            String state=record.optString("state"),label=UiLabels.messageState(this,state);
            String operation=record.optString("operation_id"),name=record.optString("line_name");if(name.isEmpty())name=record.optString("line_id");
            String from=record.optString("line_number"),body=record.optString("body"),card=record.optString("card_id");
            JSONObject current=service.messageLine(record.optString("line_id"));if(from.isEmpty()&&card.equals(current.optString("card_id")))from=current.optString("number");
            if(body.isEmpty()){
                JSONArray recent=Json.array(service.snapshot,"messages");TreeMap<Integer,JSONObject> parts=new TreeMap<>();
                for(int j=0;j<recent.length();j++){JSONObject event=recent.optJSONObject(j);if(event!=null&&record.optString("message_id").equals(event.optString("message_id"))&&record.optString("line_id").equals(event.optString("line_id"))&&record.optString("transport").equals(event.optString("transport"))&&event.optString("kind").equals("submitted")&&!event.optString("body").isEmpty())parts.put(event.optInt("part"),event);}
                StringBuilder retained=new StringBuilder();for(JSONObject part:parts.values()){if(retained.length()>0)retained.append("\n");if(part.optInt("part")>0)retained.append(UiLabels.messageEvent(this,part)).append("\n");retained.append(part.optString("body"));}body=retained.toString();
            }
            TextView detail=text(label+" · "+UiLabels.transport(this,record.optString("transport")),14);detail.setTextColor(UiLabels.messageColor(Json.obj("kind",state)));detail.setContentDescription("sms-operation:"+operation+":"+state);messageOperations.addView(detail);
            TextView receipt=text(getString(R.string.message_from,from.isEmpty()?getString(R.string.number_unavailable):from)+"\n"+getString(R.string.message_to,record.optString("recipient"))+"\n"+getString(R.string.message_line,name+" · "+UiLabels.cardSuffix(this,card))+"\n"+(body.isEmpty()?getString(R.string.message_body_unavailable):body),15);receipt.setTextIsSelectable(true);messageOperations.addView(receipt);
            if(!record.optString("failure_detail").isEmpty()){
                TextView failure=text(record.optString("failure_detail"),13);failure.setTextColor(UiLabels.ERROR);failure.setTextIsSelectable(true);messageOperations.addView(failure);
            }
            if(!MessageJournal.resolved(record)){Button check=button(messageOperations,getString(R.string.check_original_message),()->service.reconcileMessage(scope,operation));check.setId(R.id.message_reconcile);check.setContentDescription("reconcile:"+operation);check.setEnabled(service.canQueryMessages()&&!record.optString("body").isEmpty()&&!service.messageChecking(scope,operation));if(record.optString("body").isEmpty())messageOperations.addView(text(getString(R.string.message_legacy_payload_missing),12));}
        }
        if(other>0)messageOperations.addView(text(getString(R.string.other_message_records,other),12));
        if(operations.length()>0&&!service.canQueryMessages())button(messageOperations,getString(R.string.resume_for_message_check),()->{requestNotification();startAvailability();});
        if(!notifications)messageOperations.addView(text(getString(R.string.notifications_suppressed),12));
    }
    private void historyDialog(){
        JSONObject line=selected();historyDialog(line.optString("id"),route(),"",line.optString("name",line.optString("id")));
    }
    private void closeHistory(){historyEpoch++;if(activeHistoryDialog!=null){activeHistoryDialog.dismiss();activeHistoryDialog=null;}}
    private void bindHistory(AlertDialog dialog,long account){activeHistoryDialog=dialog;historyAccountEpoch=account;dialog.setOnDismissListener(d->{if(activeHistoryDialog==dialog){activeHistoryDialog=null;historyEpoch++;}});}
    private void conversationDialog(){
        if(service==null)return;closeHistory();AgentService owner=service;long account=owner.accountEpoch();ListView list=new ListView(this);list.setId(R.id.message_conversation_list);TextView status=text(getString(R.string.reading_conversations),14);LinearLayout box=new LinearLayout(this);box.setOrientation(LinearLayout.VERTICAL);box.addView(status);box.addView(list,new LinearLayout.LayoutParams(-1,Math.min(dp(320),getResources().getDisplayMetrics().heightPixels/2)));
        AlertDialog dialog=new AlertDialog.Builder(this).setTitle(getString(R.string.all_conversations)).setView(box).setNegativeButton(getString(R.string.close),null).create();bindHistory(dialog,account);dialog.show();
        owner.messageConversations(account,page->{if(!dialog.isShowing()||owner!=service||account!=owner.accountEpoch())return;JSONArray rows=Json.array(page,"conversations");ArrayList<String> labels=new ArrayList<>();for(int i=0;i<rows.length();i++){JSONObject row=rows.optJSONObject(i);if(row!=null)labels.add(row.optString("peer")+" · "+UiLabels.transport(this,row.optString("transport"))+"\n"+lineLabel(owner.messageLine(row.optString("line_id")))+"\n"+getString(R.string.message_count,row.optInt("count")));}list.setAdapter(new ArrayAdapter<>(this,android.R.layout.simple_list_item_1,labels));status.setText(getString(R.string.conversation_count,rows.length()));status.setTextColor(UiLabels.OK);list.setOnItemClickListener((parent,view,index,id)->{JSONObject row=rows.optJSONObject(index);if(row==null)return;dialog.dismiss();historyDialog(row.optString("line_id"),row.optString("transport"),row.optString("peer"),row.optString("peer"));});},failure->{if(dialog.isShowing()){status.setText(failure);status.setTextColor(UiLabels.ERROR);}});
    }
    private void historyDialog(String line,String mode,String peer,String title){
        if(service==null)return;closeHistory();AgentService owner=service;final long account=owner.accountEpoch();
        LinearLayout box=new LinearLayout(this);box.setOrientation(LinearLayout.VERTICAL);box.setPadding(dp(16),dp(8),dp(16),dp(8));
        TextView status=text(getString(R.string.reading_history),14);box.addView(status);ListView list=new ListView(this);list.setId(R.id.message_history_list);box.addView(list,new LinearLayout.LayoutParams(-1,Math.min(dp(320),getResources().getDisplayMetrics().heightPixels/2)));Button more=command(getString(R.string.older_messages));more.setId(R.id.message_history_more);box.addView(more);
        AlertDialog dialog=new AlertDialog.Builder(this).setTitle(getString(R.string.message_history_title,title)).setView(box).setNegativeButton(getString(R.string.close),null).create();bindHistory(dialog,account);
        String[] before={""};ArrayList<JSONObject> messages=new ArrayList<>();long epoch=++historyEpoch;
        Runnable load=()->{
            if(owner!=service||account!=owner.accountEpoch()){dialog.dismiss();return;}
            more.setEnabled(false);
            owner.messageHistory(line,mode,peer,before[0],account,page->{
                if(!dialog.isShowing()||historyEpoch!=epoch)return;
                JSONArray rows=Json.array(page,"messages");ArrayList<JSONObject> older=new ArrayList<>();
                for(int i=0;i<rows.length();i++){JSONObject m=rows.optJSONObject(i);if(m!=null)older.add(m);}
                int previous=messages.size();messages.addAll(0,older);before[0]=page.optString("next_before");
                JSONArray visible=MessageJournal.history(new JSONArray(messages));
                list.setAdapter(new BaseAdapter(){
                    public int getCount(){return visible.length();}
                    public Object getItem(int position){return visible.optJSONObject(position);}
                    public long getItemId(int position){return position;}
                    public View getView(int position,View reuse,ViewGroup parent){return messageRow(visible.optJSONObject(position),owner,account);}
                });
                list.setSelection(previous==0?Math.max(0,visible.length()-1):0);
                more.setEnabled(!before[0].isEmpty());status.setText(getString(R.string.message_count,visible.length()));status.setTextColor(UiLabels.OK);
            },failure->{if(dialog.isShowing()&&historyEpoch==epoch){status.setText(failure);status.setTextColor(UiLabels.ERROR);more.setEnabled(true);}});
        };
        more.setOnClickListener(v->load.run());dialog.show();load.run();
    }
    private View messageRow(JSONObject event,AgentService owner,long account){
        LinearLayout row=new LinearLayout(this);row.setOrientation(LinearLayout.VERTICAL);row.setPadding(dp(8),dp(10),dp(8),dp(10));
        String peer=UiLabels.messagePeer(event),mode=event.optString("transport"),lineID=event.optString("line_id");
        JSONObject line=owner.messageLine(lineID);String card=line.optString("card_id");
        TextView state=text(UiLabels.messageEvent(this,event)+" · "+UiLabels.transport(this,mode)+" · "+event.optString("received_at",event.optString("observed_at")),12);state.setTextColor(UiLabels.messageColor(event));row.addView(state);
        TextView from=text(getString(event.optString("kind").equals("received")?R.string.message_from:R.string.message_to,peer.isEmpty()?getString(R.string.number_unavailable):peer),15);from.setTextIsSelectable(true);row.addView(from);
        TextView destination=text(getString(R.string.message_line,lineLabel(line)),13);destination.setTextIsSelectable(true);row.addView(destination);
        TextView body=text(event.optString("body"),16);body.setTextIsSelectable(true);row.addView(body);
        String reason=event.optString("error",event.optString("error_code"));
        if(!reason.isEmpty()){TextView detail=text(reason,13);detail.setTextColor(UiLabels.messageColor(event));detail.setTextIsSelectable(true);row.addView(detail);}
        if(event.optString("kind").equals("received")){
            Button reply=button(row,getString(R.string.message_reply),()->{
                if(service!=owner||owner.accountEpoch()!=account){error(getString(R.string.history_account_changed));return;}
                String eventCard=event.optString("card_id");
                if(card.isEmpty()||!eventCard.isEmpty()&&!eventCard.equals(card)||!mode.equals("vowifi")&&!mode.equals("cellular")){error(getString(R.string.message_reply_unavailable));return;}
                final String target;try{target=CallPlan.dialTarget(peer);}catch(Exception e){error(getString(R.string.message_reply_unavailable));return;}
                Runnable prepare=()->owner.messageReplyLine(lineID,card,account,fresh->{
                    if(isDestroyed()||service!=owner||owner.accountEpoch()!=account)return;
                    Runnable compose=()->{if(service!=owner||owner.accountEpoch()!=account)return;closeHistory();number=message=null;lineChoice=null;routeChoice=null;
                        pinnedLine=fresh;selectedLine=lineID;selectedCard=card;selectedRoute=mode.equals("cellular")?1:0;draftNumber=target;draftMessage="";tab=2;render();contentScroll.scrollTo(0,0);message.requestFocus();};
                    captureDraft();if(!draftMessage.trim().isEmpty())confirm(getString(R.string.message_reply_replace),compose);else compose.run();
                },this::error);
                prepare.run();
            });reply.setId(R.id.message_reply);
        }
        return row;
    }
    private void updateMessages(){
        if(messageList==null||service==null)return;JSONArray messages=MessageJournal.history(Json.array(service.snapshot,"messages"));StringBuilder key=new StringBuilder(messages.toString()).append(service.accountEpoch());
        for(int i=0;i<messages.length();i++){JSONObject event=messages.optJSONObject(i);if(event!=null)key.append(lineLabel(service.messageLine(event.optString("line_id"))));}
        if(messageList.getChildCount()>0&&key.toString().equals(messageViewKey))return;messageViewKey=key.toString();messageList.removeAllViews();
        for(int i=0;i<messages.length();i++){JSONObject event=messages.optJSONObject(i);if(event!=null)messageList.addView(messageRow(event,service,service.accountEpoch()));}
    }
    private void captureDraft(){if(number!=null)draftNumber=number.getText().toString();if(message!=null)draftMessage=message.getText().toString();if(routeChoice!=null)selectedRoute=routeChoice.getCheckedButtonId()==R.id.route_cellular?1:0;if(lineChoice!=null){JSONObject row=selectionSnapshot.optJSONObject(lineChoice.getSelectedItemPosition());if(row!=null){selectedLine=row.optString("id");selectedCard=row.optString("card_id");}}}
    @Override protected void onSaveInstanceState(Bundle state){captureDraft();state.putInt("tab",tab);state.putString("incoming",focusIncoming);state.putString("number",draftNumber);state.putString("message",draftMessage);state.putString("line",selectedLine);state.putString("card",selectedCard);state.putInt("route",selectedRoute);super.onSaveInstanceState(state);}
    private void updateCapability(){
        if(capability==null||lineChoice==null)return;JSONObject line=selectionSnapshot.optJSONObject(lineChoice.getSelectedItemPosition());
        boolean connected=service!=null&&service.online(),ready=line!=null&&line.optBoolean("enabled")&&Json.object(Json.object(line,"operations"),route()+(tab==2?"_sms":"_call")).optBoolean("ready");
        capability.setText(!connected?R.string.connect_first:ready?(tab==2?R.string.transport_sms_ready:R.string.transport_call_ready):R.string.transport_unavailable);
        capability.setTextColor(!connected?UiLabels.WARNING:ready?UiLabels.OK:UiLabels.ERROR);
        JSONObject readiness=Json.object(Json.object(line,"operations"),route()+(tab==2?"_sms":"_call"));
        JSONArray blocked=Json.array(readiness,"blocked"),facts=Json.array(readiness,"facts");
        if(connected&&!ready)for(int i=0;i<Math.min(3,blocked.length());i++){
            String layer=blocked.optString(i);JSONObject reason=Json.obj("layer",layer);
            for(int j=0;j<facts.length();j++){JSONObject fact=facts.optJSONObject(j);if(fact!=null&&layer.equals(fact.optString("layer"))){reason=fact;break;}}
            capability.append("\n"+UiLabels.readinessReason(this,reason));
        }
        View action=findViewById(tab==2?R.id.message_send:R.id.call_dial);
        if(action!=null)action.setEnabled(connected&&ready&&(tab==2?!service.messageBusy():service.call==null));
    }
    private void updateSelection(){
        if(lineChoice==null)return;JSONObject prior=selectionSnapshot.optJSONObject(lineChoice.getSelectedItemPosition());
        String id=prior==null?selectedLine:prior.optString("id"),card=prior==null?selectedCard:prior.optString("card_id");
        JSONArray list=lines(),next=new JSONArray();ArrayList<String> labels=new ArrayList<>();int selected=-1;
        for(int i=0;i<list.length();i++){JSONObject l=list.optJSONObject(i);if(l==null)continue;if(l.optString("id").equals(id)&&l.optString("card_id").equals(card))selected=next.length();next.put(l);labels.add(lineLabel(l));}
        if(!id.isEmpty()&&selected<0){
            JSONObject retained=pinnedLine!=null&&id.equals(pinnedLine.optString("id"))&&card.equals(pinnedLine.optString("card_id"))?pinnedLine:prior;
            boolean truncated=service!=null&&service.snapshot.optBoolean("incomplete");
            if(retained!=null&&(truncated||pinnedLine==retained)){selected=next.length();next.put(retained);labels.add(lineLabel(retained)+" · "+getString(R.string.revalidate_line));}
            else{selected=next.length();next.put(Json.obj("id",id,"card_id",card,"enabled",false,"name",getString(R.string.line_unavailable)));labels.add(getString(R.string.line_unavailable));}
        }
        if(labels.isEmpty())labels.add(getString(R.string.no_lines));selectionSnapshot=next;
        if(!renderedLineLabels.equals(labels)||lineChoice.getAdapter()==null){renderedLineLabels.clear();renderedLineLabels.addAll(labels);lineChoice.setAdapter(lineAdapter(labels));}
        int position=Math.max(0,selected);if(lineChoice.getSelectedItemPosition()!=position)lineChoice.setSelection(position);
        updateDirectoryStatus();
        updateCapability();
    }
    private void updateDirectoryStatus(){
        if(directoryStatus==null)return;
        JSONArray list=lines();
        int message=service==null||!service.online()?
            (service!=null&&service.connection.resource==R.string.link_upgrade?R.string.core_mobile_endpoint_required:R.string.directory_offline):
            list.length()==0?R.string.directory_empty:0;
        if(message==0){directoryStatus.setVisibility(View.GONE);return;}
        directoryStatus.setText(message);directoryStatus.setTextColor(message==R.string.core_mobile_endpoint_required?0xffb3261e:message==R.string.directory_offline?0xff9b5b00:0xff5b6875);directoryStatus.setVisibility(View.VISIBLE);
    }
    private ArrayAdapter<String> lineAdapter(ArrayList<String> labels){
        return new ArrayAdapter<String>(this,android.R.layout.simple_spinner_item,android.R.id.text1,new ArrayList<>(labels)){
            {setDropDownViewResource(R.layout.line_dropdown_item);}
            private View wrap(View view){TextView text=view.findViewById(android.R.id.text1);text.setSingleLine(false);text.setMaxLines(Integer.MAX_VALUE);text.setEllipsize(null);text.setTextSize(14);text.setMinHeight(dp(48));text.setPadding(dp(8),dp(6),dp(8),dp(6));ViewGroup.LayoutParams size=text.getLayoutParams();if(size!=null){size.height=ViewGroup.LayoutParams.WRAP_CONTENT;text.setLayoutParams(size);}return view;}
            @Override public View getView(int position,View convert,ViewGroup parent){return wrap(super.getView(position,convert,parent));}
            @Override public View getDropDownView(int position,View convert,ViewGroup parent){return wrap(super.getDropDownView(position,convert,parent));}
        };
    }
    private void styleAudioToggle(MaterialButton button,int icon){
        button.setText("");button.setIconResource(icon);button.setIconGravity(MaterialButton.ICON_GRAVITY_TEXT_START);button.setIconPadding(0);button.setIconSize(dp(24));button.setMinimumHeight(dp(48));
        int[][] states={new int[]{-android.R.attr.state_enabled},new int[]{android.R.attr.state_checked},new int[]{}};
        button.setIconTint(new ColorStateList(states,new int[]{0xff7b898f,0xff0c6b64,0xff244e63}));
        button.setBackgroundTintList(new ColorStateList(states,new int[]{0xffedf0f2,0xffdfefed,0xffffffff}));
    }
    private String callLineIdentity(RemoteCall call){return call.plan.lineName+"\n"+(call.plan.lineNumber.isEmpty()?getString(R.string.number_unavailable):call.plan.lineNumber)+" · "+UiLabels.cardSuffix(this,call.plan.card);}
    private void directoryDialog(){
        if(service==null){error(getString(R.string.connect_first));return;}
        LinearLayout box=new LinearLayout(this);box.setPadding(dp(16),dp(8),dp(16),dp(8));box.setOrientation(LinearLayout.VERTICAL);
        EditText query=new EditText(this);query.setHint(getString(R.string.search_line_hint));box.addView(query);
        TextView result=text("",14);box.addView(result);
        ListView list=new ListView(this);box.addView(list,new LinearLayout.LayoutParams(-1,dp(280)));
        Button search=command(getString(R.string.search));box.addView(search);Button more=command(getString(R.string.more));more.setEnabled(false);box.addView(more);
        AlertDialog dialog=new AlertDialog.Builder(this).setTitle(getString(R.string.all_lines)).setView(box).setNegativeButton(R.string.cancel,null).create();
        ArrayList<JSONObject> found=new ArrayList<>();ArrayList<String> labels=new ArrayList<>();String[] after={""},searchText={""};long[] version={0};
        java.util.function.Consumer<Boolean> load=next->{long epoch=++version[0];if(!next){found.clear();labels.clear();after[0]="";searchText[0]=query.getText().toString();}
            search.setEnabled(false);more.setEnabled(false);AgentService owner=service;if(owner==null)return;
            owner.directory(searchText[0],after[0],page->{if(!dialog.isShowing()||version[0]!=epoch)return;JSONArray rows=Json.array(page,"lines");for(int i=0;i<rows.length();i++){JSONObject row=rows.optJSONObject(i);if(row!=null){found.add(row);labels.add(lineLabel(row));}}after[0]=page.optString("next_after");list.setAdapter(new ArrayAdapter<>(this,android.R.layout.simple_list_item_1,labels));search.setEnabled(true);more.setEnabled(!after[0].isEmpty());result.setText(getString(R.string.line_count,found.size()));},failure->{if(!dialog.isShowing()||version[0]!=epoch)return;search.setEnabled(true);result.setText(failure);});};
        search.setOnClickListener(v->load.accept(false));more.setOnClickListener(v->load.accept(true));
        list.setOnItemClickListener((parent,view,index,rowID)->{JSONObject chosen=found.get(index);captureDraft();pinnedLine=chosen;selectedLine=chosen.optString("id");selectedCard=chosen.optString("card_id");lineChoice=null;dialog.dismiss();render();});
        dialog.show();load.accept(false);
    }
    private void readerPage(){
        readerSummary=text("",15);readerSummary.setId(R.id.reader_status_summary);content.addView(readerSummary);
        readerUsbState=text("",14);readerUsbState.setId(R.id.reader_usb_state);content.addView(readerUsbState);
        sharingButton=button(R.string.share,()->{if(service==null)return;if(service.sharing())service.shareReaders(false);else confirm(getString(R.string.consent_share),()->service.shareReaders(true));});
        readerItems=new LinearLayout(this);readerItems.setId(R.id.reader_items);readerItems.setOrientation(LinearLayout.VERTICAL);content.addView(readerItems);
        usbPermissions=new LinearLayout(this);usbPermissions.setOrientation(LinearLayout.VERTICAL);content.addView(usbPermissions);updateReaderPage();
        button(R.string.refresh,()->{if(service!=null)service.refreshReaderMetadata();});button(R.string.help,()->error(getString(R.string.help_text)));
    }
    private void updateReaderPage(){
        if(readerSummary==null)return;
        updateUsbReaderStatus(readerUsbState);
        if(service==null){readerSummary.setText(R.string.local_service_connecting);sharingButton.setEnabled(false);return;}
        ArrayList<UsbDevice> devices=attachedCcidReaders();UsbManager usb=getSystemService(UsbManager.class);int pending=pendingReaderCount();
        String readerStateText=!service.available()?(service.sharing()?getString(R.string.sharing_paused):getString(R.string.sharing_off)):
            service.sharing()&&!service.readerLinkOnline?service.readerConnection.render(this):
            service.sharing()?service.readerStatus.render(this):getString(devices.isEmpty()?R.string.sharing_off:R.string.reader_usb_not_shared);
        if(service.sharing()&&!service.readerLinkOnline&&!service.readerLinkDiagnostic().isEmpty())readerStateText+="\n"+service.readerLinkDiagnostic();
        boolean usbFault=service.readerUSBFailures.values().stream().anyMatch(failure->failure.resource!=R.string.reader_usb_recovering);
        readerSummary.setText(getString(R.string.reader_status_summary,readerStateText,devices.size(),pending));readerSummary.setTextColor(usbFault?UiLabels.ERROR:!service.readerUSBFailures.isEmpty()?UiLabels.WARNING:service.sharing()&&!service.readerLinkOnline?UiLabels.statusColor(service.readerConnection):pending>0?UiLabels.WARNING:UiLabels.statusColor(service.readerStatus));
        sharingButton.setText(service.sharing()?getString(R.string.stop_sharing):getString(R.string.share));sharingButton.setEnabled(!service.intentSaving()&&(service.available()||service.sharing()));
        StringBuilder key=new StringBuilder();
        for(UsbDevice device:devices)key.append(device.getDeviceId()).append(':').append(device.getVendorId()).append(':').append(device.getProductId()).append(':').append(usb.hasPermission(device)).append(';');
        if(usbPermissions!=null&&!key.toString().equals(usbPermissionState)){
            usbPermissionState=key.toString();usbPermissions.removeAllViews();
            for(UsbDevice device:devices){boolean ccid=false;for(int i=0;i<device.getInterfaceCount();i++)ccid|=device.getInterface(i).getInterfaceClass()==11;if(!ccid||usb.hasPermission(device))continue;button(usbPermissions,getString(R.string.usb_permission)+" · "+device.getVendorId()+":"+device.getProductId(),()->{Intent intent=new Intent(getPackageName()+".USB_PERMISSION").setPackage(getPackageName());int flags=PendingIntent.FLAG_UPDATE_CURRENT|(Build.VERSION.SDK_INT>=31?PendingIntent.FLAG_MUTABLE:0);usb.requestPermission(device,PendingIntent.getBroadcast(this,device.getDeviceId(),intent,flags));});}
        }
        JSONArray readers=Json.array(service.readers(),"readers");String itemState=key+"|"+service.sharing()+"|"+service.available()+"|"+service.readerLinkOnline+"|"+readers;
        Map<String,UiText> failures=service.readerUSBFailures;
        for(UsbDevice device:devices){UiText failure=failures.get(usbReaderName(device));if(failure!=null)itemState+="|"+device.getDeviceId()+":"+failure.render(this);}
        if(itemState.equals(readerItemsState))return;readerItemsState=itemState;readerItems.removeAllViews();Set<String> scanned=new HashSet<>();
        for(int i=0;i<readers.length();i++){JSONObject row=readers.optJSONObject(i);if(row!=null)scanned.add(row.optString("reader_name"));}
        for(UsbDevice device:devices){String name=usbReaderName(device);if(scanned.contains(name))continue;
            int stateText=!usb.hasPermission(device)?R.string.reader_usb_wait_permission:
                !service.available()?R.string.reader_usb_paused:
                !service.sharing()?R.string.reader_usb_not_shared:
                !service.readerLinkOnline?R.string.reader_usb_wait_link:R.string.reader_usb_wait_scan;
            UiText failure=failures.get(name);
            String description=failure!=null&&usb.hasPermission(device)&&service.available()&&service.sharing()?failure.render(this):getString(stateText);
            TextView pendingRow=text(getString(R.string.reader_usb_detected,device.getVendorId(),device.getProductId())+"\n"+description,15);pendingRow.setTextColor(failure!=null&&failure.resource!=R.string.reader_usb_recovering?UiLabels.ERROR:UiLabels.WARNING);readerItems.addView(pendingRow);
        }
        for(int i=0;i<readers.length();i++){
            JSONObject reader=readers.optJSONObject(i);if(reader==null)continue;String card=reader.optString("card_id");JSONObject sim=Json.object(reader,"sim");
            String state=sim.optString("identity_state"),description=getString(state.equals("ready")?R.string.reader_identity_ready:state.equals("partial")?R.string.reader_identity_partial:state.equals("pin_required")?R.string.reader_identity_pin:R.string.reader_identity_unavailable);
            String details=reader.optString("reader_name")+" · …"+card.substring(Math.max(0,card.length()-4))+"\n"+description+"\n"+getString(R.string.reader_identity_local_only);
            if(!service.readerLinkOnline)details+="\n"+getString(R.string.reader_usb_wait_link);
            TextView row=text(details,15);
            row.setTextColor(!service.readerLinkOnline?UiLabels.WARNING:state.equals("ready")?UiLabels.OK:state.equals("unavailable")?UiLabels.ERROR:UiLabels.WARNING);
            row.setOnClickListener(v->error(sim.optString("error_code",description)));readerItems.addView(row);
        }
    }
    private void updateUsbReaderStatus(TextView view){
        if(view==null)return;
        if(service==null){view.setText(R.string.local_service_connecting);view.setTextColor(UiLabels.NEUTRAL);return;}
        ArrayList<UsbDevice> devices=attachedCcidReaders();
        if(devices.isEmpty()){view.setText(R.string.reader_usb_none);view.setTextColor(UiLabels.NEUTRAL);return;}
        UsbManager usb=getSystemService(UsbManager.class);
        JSONArray readers=Json.array(service.readers(),"readers");
        Map<String,UiText> failures=service.readerUSBFailures;
        StringBuilder details=new StringBuilder();boolean fault=false,pending=false;
        for(UsbDevice device:devices){
            String name=usbReaderName(device);JSONObject matched=null;
            for(int i=0;i<readers.length();i++){JSONObject row=readers.optJSONObject(i);if(row!=null&&name.equals(row.optString("reader_name"))){matched=row;break;}}
            UiText failure=failures.get(name);String state;
            if(!usb.hasPermission(device)){state=getString(R.string.reader_usb_wait_permission);pending=true;}
            else if(!service.available()){state=getString(R.string.reader_usb_paused);pending=true;}
            else if(!service.sharing()){state=getString(R.string.reader_usb_not_shared);pending=true;}
            else if(failure!=null){state=failure.render(this);if(failure.resource==R.string.reader_usb_recovering)pending=true;else fault=true;}
            else if(matched!=null){
                String identity=Json.object(matched,"sim").optString("identity_state");
                int label=identity.equals("ready")?R.string.reader_identity_ready:identity.equals("pin_required")?R.string.reader_identity_pin:identity.equals("partial")?R.string.reader_identity_partial:R.string.reader_identity_unavailable;
                state=getString(label)+" · "+UiLabels.cardSuffix(this,matched.optString("card_id"));
                pending|=!identity.equals("ready");
            }else{state=getString(service.readerLinkOnline?R.string.reader_usb_wait_scan:R.string.reader_usb_wait_link);pending=true;}
            if(service.available()&&service.sharing()&&!service.readerLinkOnline&&(matched!=null||failure!=null)){state+="\n"+getString(R.string.reader_usb_wait_link);pending=true;}
            if(details.length()>0)details.append("\n\n");
            details.append(getString(R.string.reader_usb_detected,device.getVendorId(),device.getProductId())).append("\n").append(state);
        }
        if(fault)details.append("\n").append(getString(R.string.reader_usb_reconnect_hint));
        view.setText(details);view.setTextColor(fault?UiLabels.ERROR:pending?UiLabels.WARNING:UiLabels.OK);
    }
    private void section(int title){TextView label=text(getString(title),14);label.setTypeface(null,android.graphics.Typeface.BOLD);label.setPadding(0,dp(16),0,dp(6));content.addView(label);}
    private void diagnostics(){
        AgentDiagnostics snapshot=AgentDiagnostics.capture(this,service);TextView body=text(snapshot.display(this),14);body.setId(R.id.diagnostics_content);body.setTextIsSelectable(true);body.setPadding(dp(20),dp(12),dp(20),dp(12));
        ScrollView scroll=new ScrollView(this);scroll.addView(body);
        new AlertDialog.Builder(this).setTitle(R.string.diagnostics_title).setView(scroll).setNegativeButton(R.string.close,null)
            .setNeutralButton(R.string.refresh,(dialog,which)->diagnostics()).setPositiveButton(R.string.share_diagnostics,(dialog,which)->{
                Intent share=new Intent(Intent.ACTION_SEND).setType("text/plain").putExtra(Intent.EXTRA_TEXT,snapshot.share());startActivity(Intent.createChooser(share,getString(R.string.diagnostics)));
            }).show();
    }
    private void settingsPage(JSONObject c){
        section(R.string.connection_section);if(!c.optString("server").isEmpty())label(c.optString("server"));label(getString(c.optString("pin").isEmpty()?R.string.tls_system:R.string.tls_pinned));
        button(R.string.gateway,()->startActivity(new Intent(Intent.ACTION_VIEW,Uri.parse(c.optString("server")))));
        section(R.string.device_section);label(AgentDiagnostics.version(this));String revision=getString(R.string.source_revision);label(getString(R.string.revision_label,revision.equals("unknown")?getString(R.string.unknown):revision.substring(0,12)));
        button(content,getString(R.string.diagnostics),this::diagnostics).setId(R.id.diagnostics_open);
        button(R.string.battery,()->{try{startActivity(new Intent(Settings.ACTION_IGNORE_BATTERY_OPTIMIZATION_SETTINGS));}catch(Exception e){startActivity(new Intent(Settings.ACTION_APPLICATION_DETAILS_SETTINGS,Uri.parse("package:"+getPackageName())));}});
        section(R.string.account_section);
        button(content,getString(R.string.sign_in_again),()->{if(service!=null&&service.accountBusy()){error(getString(R.string.account_busy));return;}cancelLogin();if(service!=null)service.prepareLogin();ConfigStore.intent(()->{try{new ConfigStore(this).update(current->{current.remove("token");current.remove("csrf");});runOnUiThread(()->{tab=0;reloadStorage();});}catch(Exception failure){runOnUiThread(()->error(getString(R.string.settings_save_failed)));}});}).setId(R.id.sign_in_again);
        button(content,getString(R.string.forget_login),()->{if(loginProfile==null)loginProfile=LoginProfile.read(savedConfig);loginProfile.password="";loginProfile.remember=false;loginVersion++;setLoginBusy(false);persistLoginDraft(()->error(getString(R.string.password_forgotten)));}).setId(R.id.forget_login);
        button(R.string.logout,()->confirm(getString(R.string.logout_confirm),()->{cancelLogin();if(service!=null)service.logout(this::reloadStorage);})).setId(R.id.logout);button(R.string.help,()->error(getString(R.string.help_text)));}
    private void updateState(){if(status==null)return;if(!storageLoaded||!storageError.isEmpty())return;if(activeHistoryDialog!=null&&(service==null||service.accountEpoch()!=historyAccountEpoch))closeHistory();status.setText(service==null?getString(R.string.local_service_connecting):service.connection.render(this));notice.setText(service==null?"":service.notice.render(this));notice.setVisibility(notice.length()==0?View.GONE:View.VISIBLE);status.setTextColor(service==null?UiLabels.NEUTRAL:UiLabels.statusColor(service.connection));notice.setTextColor(service==null?UiLabels.NEUTRAL:UiLabels.statusColor(service.notice));updateDirectoryStatus();updateCapability();updateReaderPage();updateReaderBadge();updateHomePage();updateMessageOperations();if(service==null){callSurfaceKey="";callBox.removeAllViews();return;}
        int availabilityLabel=service.available()?R.string.pause:R.string.resume;
        for(int id:new int[]{R.id.availability_toggle,R.id.home_availability_toggle}){
            Button availability=findViewById(id);if(availability!=null){availability.setText(availabilityLabel);availability.setEnabled(!service.intentSaving());}
        }
        if(lastData!=service.snapshot){lastData=service.snapshot;if(tab==0){updateHomePage();}else{updateSelection();updateMessages();}}
        RemoteCall current=service.call;
        updateToneDialog();
        boolean hasAudio=current!=null&&current.audio!=null&&!current.audio.closed;
        if(current!=renderedCall||hasAudio!=renderedAudioControls){callSurfaceKey="";if(current==null&&renderedCall!=null&&renderedCall.plan.incoming!=null)consumeIncoming(renderedCall.plan.mode,renderedCall.plan.incoming);renderedCall=current;renderedAudioControls=hasAudio;if(tab==1){render();return;}}
        if(hasAudio){MaterialButton mute=findViewById(R.id.call_mute),speaker=findViewById(R.id.call_speaker);View dtmf=findViewById(R.id.call_dtmf);
            if(mute!=null){boolean checked=current.audio.muted;mute.setChecked(checked);mute.setIconResource(checked?R.drawable.ic_mdd_mic_off:R.drawable.ic_mdd_mic);mute.setContentDescription(getString(checked?R.string.unmute:R.string.mute));mute.setTooltipText(mute.getContentDescription());}
            if(speaker!=null){boolean checked=current.audio.speakerEnabled();speaker.setChecked(checked);speaker.setIconResource(checked?R.drawable.ic_mdd_volume_up:R.drawable.ic_mdd_hearing);speaker.setContentDescription(getString(checked?R.string.earpiece:R.string.speaker));speaker.setTooltipText(speaker.getContentDescription());}
            if(dtmf!=null)dtmf.setEnabled(current.audio.active);}
        if(current!=null){
            String callState=current.state.render(this),audioState=current.audio==null?getString(R.string.audio_off):current.audio.description().render(this);
            if(callAudioDetails!=null){callAudioDetails.setText(audioState);callAudioDetails.setTextColor(current.audio==null?UiLabels.WARNING:UiLabels.statusColor(current.audio.description()));callAudioDetails.setVisibility(audioState.isEmpty()?View.GONE:View.VISIBLE);}
            String key="active:"+current.plan.operation+":"+tab+":"+callState+":"+audioState;if(key.equals(callSurfaceKey))return;callSurfaceKey=key;callBox.removeAllViews();
            LinearLayout summary=new LinearLayout(this);summary.setGravity(Gravity.CENTER_VERTICAL);
            String identity=callLineIdentity(current);
            String compactIdentity=(current.plan.lineNumber.isEmpty()?UiLabels.cardSuffix(this,current.plan.card):current.plan.lineNumber)+" · "+current.plan.number+" · "+UiLabels.transport(this,current.plan.mode);
            LinearLayout details=new LinearLayout(this);details.setOrientation(LinearLayout.VERTICAL);
            TextView who=text(compactIdentity,12);who.setMaxLines(1);who.setEllipsize(android.text.TextUtils.TruncateAt.END);details.addView(who);
            TextView detail=text(callState,14);detail.setTextColor(UiLabels.statusColor(current.state));detail.setMaxLines(2);detail.setEllipsize(android.text.TextUtils.TruncateAt.END);details.addView(detail);
            detail.setOnClickListener(v->{if(tab!=1){tab=1;render();}else error(identity+"\n"+current.plan.number+"\n"+callState+(audioState.isEmpty()?"":"\n"+audioState));});
            who.setOnClickListener(v->detail.performClick());summary.addView(details,new LinearLayout.LayoutParams(0,-2,1));
            if(tab!=1){MaterialButton endCall=command(getString(R.string.hangup));endCall.setId(R.id.call_hangup);endCall.setTextColor(0xffb3261e);endCall.setOnClickListener(v->current.hangup());summary.addView(endCall,new LinearLayout.LayoutParams(dp(96),-2));}
            callBox.addView(summary);return;
        }
        JSONArray rows=Json.array(service.snapshot,service.snapshot.has("incoming_lines")?"incoming_lines":"lines");
        for(int i=0;i<rows.length();i++){JSONObject line=rows.optJSONObject(i);if(line==null)continue;
            JSONObject vowifi=line.optJSONObject("incoming"),cell=line.optJSONObject("cellular_incoming");
            if(vowifi!=null&&(focusIncoming.isEmpty()||focusIncoming.equals("call:"+vowifi.optString("call_id")))){updateIncomingSurface(line,"vowifi",vowifi);return;}
            if(cell!=null&&cell.optBoolean("actionable")&&(focusIncoming.isEmpty()||focusIncoming.equals("cell:"+cell.optString("incoming_event_id")))){updateIncomingSurface(line,"cellular",cell);return;}
        }
        String empty="empty:"+focusIncoming+":"+service.snapshot.optBoolean("incoming_more");if(empty.equals(callSurfaceKey))return;callSurfaceKey=empty;callBox.removeAllViews();
        if(!focusIncoming.isEmpty())callBox.addView(text(getString(R.string.incoming_changed),14));
        if(service.snapshot.optBoolean("incoming_more")||!focusIncoming.isEmpty())button(callBox,getString(R.string.all_incoming),this::incomingDialog);

    }
    private void incomingDialog(){
        if(service==null)return;AgentService owner=service;LinearLayout box=new LinearLayout(this);box.setOrientation(LinearLayout.VERTICAL);
        TextView status=text("",14);box.addView(status);ListView list=new ListView(this);box.addView(list,new LinearLayout.LayoutParams(-1,dp(280)));
        Button more=command(getString(R.string.more_incoming));box.addView(more);AlertDialog dialog=new AlertDialog.Builder(this).setTitle(getString(R.string.all_incoming)).setView(box).setNegativeButton(getString(R.string.close),null).create();
        ArrayList<JSONObject> rows=new ArrayList<>();ArrayList<String> labels=new ArrayList<>();String[] after={""};
        Runnable load=()->{more.setEnabled(false);owner.incomingPage(after[0],page->{if(!dialog.isShowing())return;JSONArray items=Json.array(page,"lines");for(int i=0;i<items.length();i++){JSONObject line=items.optJSONObject(i);if(line!=null){for(String field:new String[]{"incoming","cellular_incoming"}){JSONObject event=line.optJSONObject(field);if(event!=null){rows.add(Json.obj("line",line,"mode",field.equals("incoming")?"vowifi":"cellular","event",event));labels.add(lineLabel(line)+" · "+event.optString("caller",event.optString("number")));}}}}after[0]=page.optString("next_after");list.setAdapter(new ArrayAdapter<>(this,android.R.layout.simple_list_item_1,labels));status.setText(getString(R.string.incoming_count,rows.size()));more.setEnabled(!after[0].isEmpty());},failure->{if(dialog.isShowing()){status.setText(failure);more.setEnabled(true);}});};
        list.setOnItemClickListener((parent,view,index,id)->{JSONObject row=rows.get(index),line=Json.object(row,"line"),event=Json.object(row,"event");String mode=row.optString("mode");new AlertDialog.Builder(this).setTitle(lineLabel(line)).setMessage(event.optString("caller",event.optString("number"))).setPositiveButton(R.string.answer,(d,w)->startCall(line,mode,"",event)).setNeutralButton(R.string.reject,(d,w)->rejectIncoming(line,mode,event)).setNegativeButton(R.string.cancel,null).show();});
        more.setOnClickListener(v->load.run());dialog.show();load.run();
    }
    private void consumeIncoming(String mode,JSONObject call){String key=mode.equals("cellular")?"cell:"+call.optString("incoming_event_id"):"call:"+call.optString("call_id");if(focusIncoming.equals(key)||focusIncoming.equals("all"))focusIncoming="";}
    private void updateIncomingSurface(JSONObject line,String mode,JSONObject call){String key=Json.obj("owner",service.accountEpoch(),"line",line.optString("id"),"card",line.optString("card_id"),"enabled",line.optBoolean("enabled"),"mode",mode,"call",call).toString();if(key.equals(callSurfaceKey))return;callSurfaceKey=key;callBox.removeAllViews();incoming(line,mode,call);}
    private void rejectIncoming(JSONObject line,String mode,JSONObject call){if(service!=null)service.reject(line,mode,call,()->{if(isDestroyed())return;consumeIncoming(mode,call);updateState();});}
    private void incoming(JSONObject line,String mode,JSONObject call){callBox.addView(text(getString(R.string.incoming_call)+" · "+UiLabels.transport(this,mode)+"\n"+call.optString("caller",call.optString("number")),18));LinearLayout row=new LinearLayout(this);callBox.addView(row);button(row,getString(R.string.answer),()->startCall(line,mode,"",call)).setId(R.id.call_answer);button(row,getString(R.string.reject),()->rejectIncoming(line,mode,call)).setId(R.id.call_reject);}
}
