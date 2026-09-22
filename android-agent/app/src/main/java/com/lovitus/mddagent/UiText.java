package com.lovitus.mddagent;

import android.content.Context;

/** In-memory presentation only. Resource IDs never enter durable or wire state. */
final class UiText {
    static final UiText EMPTY = new UiText(0,new Object[0]);
    final int resource;
    private final Object[] arguments;
    private UiText(int resource,Object[] arguments){this.resource=resource;this.arguments=arguments.clone();}
    static UiText of(int resource,Object... arguments){return new UiText(resource,arguments);}
    boolean empty(){return resource==0;}
    String render(Context context){
        if(empty())return "";
        Object[] values=arguments.clone();
        for(int i=0;i<values.length;i++)if(values[i] instanceof UiText)values[i]=((UiText)values[i]).render(context);
        return context.getString(resource,values);
    }
}
