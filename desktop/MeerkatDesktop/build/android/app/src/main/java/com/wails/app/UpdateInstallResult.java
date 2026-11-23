package com.wails.app;

import android.app.PendingIntent;
import android.content.*;
import android.content.pm.PackageInstaller;
import android.os.Build;
import java.io.*;

/** Explicit, non-exported receiver for the PackageInstaller session. */
public final class UpdateInstallResult extends BroadcastReceiver {
    static void commit(Context context, File apk) throws IOException {
        PackageInstaller installer=context.getPackageManager().getPackageInstaller();
        PackageInstaller.SessionParams params=new PackageInstaller.SessionParams(PackageInstaller.SessionParams.MODE_FULL_INSTALL);
        params.setAppPackageName(context.getPackageName());
        params.setSize(apk.length());
        if(Build.VERSION.SDK_INT>=31) params.setRequireUserAction(PackageInstaller.SessionParams.USER_ACTION_REQUIRED);
        int id=installer.createSession(params);
        try(PackageInstaller.Session session=installer.openSession(id)) {
            try(InputStream in=new FileInputStream(apk);OutputStream out=session.openWrite("base.apk",0,apk.length())) {
                byte[] buf=new byte[65536];int n;while((n=in.read(buf))!=-1) out.write(buf,0,n);session.fsync(out);
            }
            Intent result=new Intent(context,UpdateInstallResult.class);
            int flags=PendingIntent.FLAG_UPDATE_CURRENT|(Build.VERSION.SDK_INT>=31?PendingIntent.FLAG_MUTABLE:0);
            session.commit(PendingIntent.getBroadcast(context,id,result,flags).getIntentSender());
        }catch(IOException|RuntimeException e){installer.abandonSession(id);throw e;}
    }
    @Override public void onReceive(Context context,Intent intent) {
        int status=intent.getIntExtra(PackageInstaller.EXTRA_STATUS,PackageInstaller.STATUS_FAILURE);
        String text;
        if(status==PackageInstaller.STATUS_PENDING_USER_ACTION) {
            Intent confirm=intent.getParcelableExtra(Intent.EXTRA_INTENT);
            if(confirm!=null) {try{context.startActivity(confirm.addFlags(Intent.FLAG_ACTIVITY_NEW_TASK));return;}catch(RuntimeException ignored){}}
            text="无法打开系统安装确认，请回到设置后重试";
        }else if(status==PackageInstaller.STATUS_SUCCESS) {text="更新已安装";}
        else if(status==PackageInstaller.STATUS_FAILURE_ABORTED){text="已取消安装，安装包已保留，可再次安装";}
        else if(status==PackageInstaller.STATUS_FAILURE_STORAGE){text="手机存储空间不足，请清理后重试";}
        else {text="系统安装失败（"+status+"），安装包已保留";}
        context.getSharedPreferences("app-update",0).edit().putString("result",text).apply();
    }
}
