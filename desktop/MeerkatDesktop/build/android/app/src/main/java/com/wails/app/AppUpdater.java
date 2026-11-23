package com.wails.app;

import android.content.*;
import android.content.pm.*;
import android.net.*;
import android.os.*;
import android.provider.Settings;
import android.webkit.WebView;
import androidx.activity.result.ActivityResultLauncher;
import androidx.activity.result.contract.ActivityResultContracts;
import androidx.webkit.*;
import org.json.*;
import java.io.*;
import java.nio.charset.StandardCharsets;
import java.util.Collections;
import java.util.concurrent.*;

/** The only web entry point is an origin-scoped, main-frame message listener. */
final class AppUpdater {
    private static final String ORIGIN = "https://wails.localhost";
    private static final long HOUR = 3600000L;
    private static final ExecutorService worker = Executors.newSingleThreadExecutor();
    private static final Handler ui = new Handler(Looper.getMainLooper());
    private static native byte[] runNative(String action, String directory, byte[] envelope, long code, int sdk);
    private static native void control(boolean start);
    private static native long received();
    private final MainActivity activity;
    private final SharedPreferences prefs;
    private final File directory;
    private final ConnectivityManager network;
    private final ActivityResultLauncher<Intent> permission;
    private JSONObject candidate = new JSONObject();
    private String phase = "loading", message = "";
    private boolean disposed, foreground, limited, cancelled;
    private final Runnable checkLater = () -> autoCheck();
    private final Runnable monitor = new Runnable() {
        public void run() {
            if (disposed || !phase.equals("downloading")) return;
            if (limited && !unmetered()) cancelDownload("网络已变为计费网络，下载已暂停");
            else ui.postDelayed(this, 350);
        }
    };

    AppUpdater(MainActivity activity, WebView view) {
        this.activity = activity;
        prefs = activity.getSharedPreferences("app-update", 0);
        directory = new File(activity.getCacheDir(), "updates");
        network = (ConnectivityManager)activity.getSystemService(Context.CONNECTIVITY_SERVICE);
        permission = activity.registerForActivityResult(new ActivityResultContracts.StartActivityForResult(), r -> {
            if (allowed()) install(); else set("permission", "尚未允许安装。可到系统设置开启，安装包已保留。");
        });
        if (!WebViewFeature.isFeatureSupported(WebViewFeature.WEB_MESSAGE_LISTENER)) return;
        WebViewCompat.addWebMessageListener(view, "meerkatAppUpdater", Collections.singleton(ORIGIN), (v,m,o,top,reply) -> {
            if (!top || !Uri.parse(ORIGIN).equals(o) || m.getType()!=WebMessageCompat.TYPE_STRING) return;
            try { command(m.getData()); reply.postMessage(snapshot().toString()); }
            catch (Exception e) { set("error", reason(e)); }
        });
        worker.execute(() -> {
            try {
                if (!directory.isDirectory() && !directory.mkdirs()) throw new IOException("无法创建更新缓存");
                File[] files=directory.listFiles();
                if(files!=null) for(File f:files) {
                    if(f.getName().endsWith(".part") || (f.getName().matches("meerkat-[0-9]+\\.apk") && Long.parseLong(f.getName().substring(8,f.getName().length()-4)) <= BuildConfig.VERSION_CODE)) f.delete();
                }
                String envelope=prefs.getString("envelope", "");
                if(envelope.isEmpty()) { post(() -> set("idle", "")); return; }
                JSONObject saved=run("restore",envelope);
                boolean available=saved.optBoolean("available");
                boolean ready=available && file(saved).isFile();
                if(ready) run("verify",envelope);
                post(() -> {candidate=saved;set(ready?"ready":available?"available":"latest","");});
            } catch(Exception e) { post(() -> set("error",reason(e))); }
        });
    }
    void resume() {foreground=true;ui.removeCallbacks(checkLater);ui.postDelayed(checkLater,15000);}
    void pause() {foreground=false;ui.removeCallbacks(checkLater);}
    void close() {disposed=true;pause();ui.removeCallbacks(monitor);if(phase.equals("downloading")) control(false);}
    private void post(Runnable work) {ui.post(() -> {if(!disposed) work.run();});}
    private void set(String state,String text) {phase=state;message=text;}
    private static String reason(Exception e) {return e.getMessage()==null?"操作失败，请重试":e.getMessage();}
    private boolean auto() {return prefs.getBoolean("auto",true);}
    private boolean wifi() {return prefs.getBoolean("wifi",true);}
    private boolean unmetered() {return network!=null && network.getActiveNetworkInfo()!=null && network.getActiveNetworkInfo().isConnected() && !network.isActiveNetworkMetered();}
    private boolean allowed() {return Build.VERSION.SDK_INT<26 || activity.getPackageManager().canRequestPackageInstalls();}
    private boolean busy() {return phase.equals("checking")||phase.equals("downloading")||phase.equals("verifying")||phase.equals("installing")||phase.equals("loading");}
    private int code(JSONObject result) throws JSONException {return result.getJSONObject("pkg").getInt("versionCode");}
    private File file(JSONObject result) throws JSONException {return new File(directory,"meerkat-"+code(result)+".apk");}
    private JSONObject run(String action,String envelope) throws Exception {
        byte[] bytes=runNative(action,directory.getAbsolutePath(),envelope.getBytes(StandardCharsets.UTF_8),BuildConfig.VERSION_CODE,Build.VERSION.SDK_INT);
        JSONObject result=new JSONObject(new String(bytes,StandardCharsets.UTF_8));
        if(result.has("error")) throw new IOException(result.getString("error"));
        return result;
    }
    private boolean busyExceptInstalling() {return busy() && !phase.equals("installing");}
    private JSONObject snapshot() throws JSONException {
        if(prefs.contains("result") && !busyExceptInstalling()) {
            String result = prefs.getString("result", "");
            prefs.edit().remove("result").apply();
            if (candidate.optBoolean("available") && file(candidate).isFile()) set("ready",result);
            else if ("更新已安装".equals(result)) set("latest",result);
        }
        JSONObject s=new JSONObject();s.put("phase",BuildConfig.DEBUG?"disabled":phase);s.put("message",message);
        s.put("current",BuildConfig.VERSION_NAME+" (build "+BuildConfig.VERSION_CODE+")");s.put("version",candidate.optString("version"));s.put("notes",candidate.optString("notes"));
        JSONObject pkg=candidate.optJSONObject("pkg");s.put("size",pkg==null?0:pkg.optLong("size"));s.put("bytes",phase.equals("downloading")?received():0);
        s.put("auto",auto());s.put("wifi",wifi());s.put("metered",!unmetered());s.put("last",prefs.getLong("success",0));return s;
    }
    private void command(String action) {
        if(BuildConfig.DEBUG || action==null) return;
        switch(action) {
        case "check": if(!busy()) check(false);break;
        case "download": case "cellular": if(phase.equals("available")) download(action.equals("cellular"));break;
        case "cancel": if(phase.equals("downloading")) cancelDownload("已取消下载");break;
        case "install": if(phase.equals("ready")||phase.equals("permission")) install();break;
        case "permission": if(phase.equals("permission")) openPermission();break;
        case "later": if(phase.equals("permission")) set("ready","");break;
        case "auto": prefs.edit().putBoolean("auto",!auto()).apply();break;
        case "wifi": if(auto()) prefs.edit().putBoolean("wifi",!wifi()).apply();break;
        default: break;
        }
    }
    private void autoCheck() {
        if(BuildConfig.DEBUG || !foreground || !auto() || busy()) return;
        if(phase.equals("available") && (!wifi()||unmetered())) {download(!wifi());return;}
        long now=System.currentTimeMillis();
        if(now-prefs.getLong("attempt",0)>=15*60000L && now-prefs.getLong("success",0)>=6*HOUR) check(true);
    }
    private void check(boolean automatic) {
        set("checking","");prefs.edit().putLong("attempt",System.currentTimeMillis()).apply();
        worker.execute(() -> {try {
            JSONObject next=run("check","");String envelope=next.getJSONObject("envelope").toString();
            boolean available=next.optBoolean("available"), ready=false;
            if(available && file(next).isFile()) {try{run("verify",envelope);ready=true;}catch(IOException invalid){file(next).delete();}}
            boolean cached=ready;
            post(() -> {candidate=next;prefs.edit().putLong("success",System.currentTimeMillis()).putString("envelope",envelope).apply();set(cached?"ready":available?"available":"latest","");if(automatic && auto() && available && !cached && (!wifi()||unmetered())) download(!wifi());});
        }catch(Exception e){post(() -> set("error","检查失败，请检查网络后重试。"+reason(e)));}});
    }
    private void cancelDownload(String text) {cancelled=true;message=text;control(false);}
    private void download(boolean useMetered) {
        if(!useMetered && !unmetered()) {set("available","当前为计费网络，请确认使用流量下载。");return;}
        limited=!useMetered;cancelled=false;control(true);set("downloading","");ui.post(monitor);
        worker.execute(() -> {try{run("download",prefs.getString("envelope",""));post(() -> set("ready","安装包校验通过"));}
            catch(Exception e){post(() -> set("available",cancelled?message:"下载失败，请重试。"+reason(e)));}});
    }
    private void openPermission() {
        if(allowed()){install();return;}
        Uri id=Uri.parse("package:"+activity.getPackageName());
        try{permission.launch(new Intent(Settings.ACTION_MANAGE_UNKNOWN_APP_SOURCES,id));}
        catch(ActivityNotFoundException e){permission.launch(new Intent(Settings.ACTION_APPLICATION_DETAILS_SETTINGS,id));}
    }
    private void install() {
        if(!allowed()){set("permission","需要开启「安装未知应用」。允许来自 Meerkat 的安装后，返回即可继续。");return;}
        set("verifying","");worker.execute(() -> {try {
            JSONObject verified=run("verify",prefs.getString("envelope",""));
            if(!verified.getBoolean("available")) throw new IOException("当前版本无需更新");
            File apk=file(verified);PackageManager pm=activity.getPackageManager();PackageInfo pkg=pm.getPackageArchiveInfo(apk.getPath(),PackageManager.GET_SIGNATURES);
            if(pkg==null || !activity.getPackageName().equals(pkg.packageName) || (Build.VERSION.SDK_INT>=28?pkg.getLongVersionCode():pkg.versionCode)!=code(verified)) throw new IOException("安装包名称或版本与清单不符");
            if(!java.util.Arrays.equals(pkg.signatures,pm.getPackageInfo(activity.getPackageName(),PackageManager.GET_SIGNATURES).signatures)) throw new IOException("安装包签名与当前应用不一致");
            if(disposed)return;
            post(() -> set("installing","请在系统窗口确认安装"));
            UpdateInstallResult.commit(activity,apk);
        }catch(Exception e){post(() -> set("error","安装失败，请重新检查更新。"+reason(e)));}});
    }
}
