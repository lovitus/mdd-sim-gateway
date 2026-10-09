package com.lovitus.mddagent;

import android.content.Context;
import android.graphics.Typeface;
import android.text.TextUtils;
import android.view.Gravity;
import android.widget.LinearLayout;
import android.widget.TextView;
import org.json.JSONObject;

/** Shared line identity and independent route-readiness presentation. */
final class LineStatusView extends LinearLayout {
    private final TextView name,summary,number,wifi,cell;
    LineStatusView(Context context){
        super(context);setOrientation(VERTICAL);setPadding(dp(8),dp(8),dp(8),dp(8));
        LinearLayout heading=new LinearLayout(context);heading.setGravity(Gravity.CENTER_VERTICAL);
        name=label(14);name.setTypeface(null,Typeface.BOLD);name.setMaxLines(2);
        heading.addView(name,new LayoutParams(0,-2,1));
        summary=label(15);summary.setId(R.id.line_availability_summary);
        summary.setTypeface(null,Typeface.BOLD);summary.setMaxWidth(dp(136));summary.setMaxLines(3);
        summary.setGravity(Gravity.END);summary.setPadding(dp(8),dp(2),0,dp(2));
        heading.addView(summary,new LayoutParams(-2,-2));addView(heading,new LayoutParams(-1,-2));
        number=label(12);number.setTextColor(UiLabels.NEUTRAL);number.setMaxLines(2);addView(number);
        LinearLayout routes=new LinearLayout(context);routes.setGravity(Gravity.CENTER_VERTICAL);
        wifi=label(13);cell=label(13);wifi.setId(R.id.line_vowifi_state);cell.setId(R.id.line_cellular_state);
        routes.addView(wifi,new LayoutParams(0,-2,1));routes.addView(cell,new LayoutParams(0,-2,1));
        addView(routes,new LayoutParams(-1,-2));
    }
    void bind(JSONObject line,boolean online,boolean sms){
        name.setText(line.optString("name",line.optString("id")));
        String value=line.optString("number"),card=line.optString("card_id");
        number.setText((value.isEmpty()?getContext().getString(R.string.number_unavailable):value)+
            (card.isEmpty()?"":" · "+UiLabels.cardSuffix(getContext(),card)));
        int wifiState=UiLabels.routeState(line,"vowifi",sms,online);
        int cellState=UiLabels.routeState(line,"cellular",sms,online);
        bindRoute(wifi,"vowifi",wifiState);bindRoute(cell,"cellular",cellState);
        bindSummary(wifiState,cellState);
    }
    private void bindRoute(TextView view,String mode,int state){
        String value=UiLabels.transport(getContext(),mode)+" · "+getContext().getString(state);
        view.setText(value);view.setTextColor(UiLabels.routeColor(state));
        view.setTypeface(null,state==R.string.route_ready?Typeface.BOLD:Typeface.NORMAL);
        view.setContentDescription(value);
    }
    private void bindSummary(int wifiState,int cellState){
        boolean wifiReady=wifiState==R.string.route_ready,cellReady=cellState==R.string.route_ready;
        int state,label;
        if(wifiReady||cellReady){
            state=R.string.route_ready;
            label=wifiReady&&cellReady?R.string.line_both_available:wifiReady?R.string.line_vowifi_available:R.string.line_cellular_available;
        }else{
            state=R.string.route_unavailable;
            if(wifiState==R.string.route_offline&&cellState==R.string.route_offline)state=R.string.route_offline;
            else if(wifiState==R.string.route_disabled&&cellState==R.string.route_disabled)state=R.string.route_disabled;
            else for(int candidate:new int[]{R.string.route_busy,R.string.route_connecting,R.string.route_unknown}){
                if(wifiState==candidate||cellState==candidate){state=candidate;break;}
            }
            label=state;
        }
        summary.setText(label);summary.setTextColor(UiLabels.routeColor(state));
    }
    private TextView label(int size){TextView view=new TextView(getContext());view.setTextSize(size);view.setTextColor(0xff172334);view.setPadding(0,dp(2),0,dp(2));view.setEllipsize(TextUtils.TruncateAt.END);return view;}
    private int dp(int value){return Math.round(value*getResources().getDisplayMetrics().density);}
}
