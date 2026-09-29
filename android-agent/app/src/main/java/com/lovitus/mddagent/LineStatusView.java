package com.lovitus.mddagent;

import android.content.Context;
import android.graphics.Typeface;
import android.text.SpannableString;
import android.text.Spanned;
import android.text.TextUtils;
import android.text.style.ForegroundColorSpan;
import android.view.Gravity;
import android.widget.LinearLayout;
import android.widget.TextView;
import org.json.JSONObject;

/** Shared line identity and independent route-readiness presentation. */
final class LineStatusView extends LinearLayout {
    private final TextView name,number,wifi,cell;
    LineStatusView(Context context){
        super(context);setOrientation(VERTICAL);setPadding(dp(8),dp(8),dp(8),dp(8));
        name=label(14);name.setTypeface(null,Typeface.BOLD);name.setMaxLines(2);addView(name);
        number=label(12);number.setTextColor(UiLabels.NEUTRAL);number.setMaxLines(2);addView(number);
        LinearLayout routes=new LinearLayout(context);routes.setGravity(Gravity.CENTER_VERTICAL);
        wifi=label(12);cell=label(12);wifi.setId(R.id.line_vowifi_state);cell.setId(R.id.line_cellular_state);
        routes.addView(wifi,new LayoutParams(0,-2,1));routes.addView(cell,new LayoutParams(0,-2,1));
        addView(routes,new LayoutParams(-1,-2));
    }
    void bind(JSONObject line,boolean online,boolean sms){
        name.setText(line.optString("name",line.optString("id")));
        String value=line.optString("number"),card=line.optString("card_id");
        number.setText((value.isEmpty()?getContext().getString(R.string.number_unavailable):value)+
            (card.isEmpty()?"":" · "+UiLabels.cardSuffix(getContext(),card)));
        bindRoute(wifi,line,"vowifi",online,sms);bindRoute(cell,line,"cellular",online,sms);
    }
    private void bindRoute(TextView view,JSONObject line,String mode,boolean online,boolean sms){
        int state=UiLabels.routeState(line,mode,sms,online);
        String value="\u25cf "+UiLabels.transport(getContext(),mode)+" · "+getContext().getString(state);
        SpannableString text=new SpannableString(value);
        text.setSpan(new ForegroundColorSpan(UiLabels.routeColor(state)),0,1,Spanned.SPAN_EXCLUSIVE_EXCLUSIVE);
        view.setText(text);view.setContentDescription(value.substring(2));
    }
    private TextView label(int size){TextView view=new TextView(getContext());view.setTextSize(size);view.setTextColor(0xff172334);view.setPadding(0,dp(2),0,dp(2));view.setEllipsize(TextUtils.TruncateAt.END);return view;}
    private int dp(int value){return Math.round(value*getResources().getDisplayMetrics().density);}
}
