package com.wails.app;

import android.app.Activity;
import android.app.Dialog;
import android.net.Uri;
import android.view.ViewGroup;
import android.webkit.WebResourceRequest;
import android.webkit.WebResourceResponse;
import android.webkit.WebView;
import android.webkit.WebViewClient;
import android.widget.Button;
import android.widget.LinearLayout;
import androidx.webkit.WebMessageCompat;
import androidx.webkit.WebViewCompat;
import androidx.webkit.WebViewFeature;
import java.io.ByteArrayInputStream;
import java.nio.charset.StandardCharsets;
import java.util.Collections;
import java.util.concurrent.atomic.AtomicLong;
import org.json.JSONObject;

/** Untrusted service documents have a separate WebView with no native interfaces. */
final class ServiceViewer {
    private static final AtomicLong nextSession = new AtomicLong();
    private final WailsBridge bridge;
    private Dialog current;
    private WebView currentPage;
    private long currentID;
    private boolean destroyed;

    ServiceViewer(Activity activity, WebView shell, WailsBridge bridge, String shellOrigin) {
        this.bridge = bridge;
        if (!WebViewFeature.isFeatureSupported(WebViewFeature.WEB_MESSAGE_LISTENER)) return;
        WebViewCompat.addWebMessageListener(shell, "meerkatServiceViewer", Collections.singleton(shellOrigin),
                (view, message, origin, mainFrame, reply) -> {
                    if (destroyed || activity.isFinishing() || activity.isDestroyed() || !mainFrame || !Uri.parse(shellOrigin).equals(origin) || message.getType() != WebMessageCompat.TYPE_STRING) return;
                    String address = message.getData();
                    if (address == null || address.length() > 4096) return;
                    long id = nextSession.incrementAndGet();
                    try {
                        byte[] response = bridge.serveAsset("/client/service/open?session=" + id + "&url=" + Uri.encode(address), "POST", "{}");
                        JSONObject result = new JSONObject(new String(response, StandardCharsets.UTF_8));
                        if (result.has("url")) {
                            dismissCurrent();
                            currentID = id;
                            show(activity, id, Uri.parse(result.getString("url")));
                            reply.postMessage(new JSONObject().put("saved", address).toString());
                        } else reply.postMessage(result.toString());
                    } catch (Exception err) {
                        if (currentID == id) dismissCurrent();
                        else release(id);
                        reply.postMessage("{\"error\":\"无法打开服务，请检查内嵌网络后重试。\"}");
                    }
                });
    }

    void destroy() {
        destroyed = true;
        dismissCurrent();
    }

    private void release(long id) {
        bridge.serveAsset("/client/service/close?session=" + id, "POST", "{}");
    }

    private void dismissCurrent() {
        if (currentID == 0) return;
        long id = currentID;
        Dialog dialog = current;
        WebView page = currentPage;
        currentID = 0;
        current = null;
        currentPage = null;
        try {
            if (dialog != null) {
                dialog.setOnDismissListener(null);
                dialog.dismiss();
            }
            if (page != null) {
                page.stopLoading();
                if (page.getParent() instanceof ViewGroup) ((ViewGroup) page.getParent()).removeView(page);
                page.destroy();
            }
        } finally {
            release(id);
        }
    }

    private void show(Activity activity, long id, Uri entry) {
        Dialog dialog = new Dialog(activity, android.R.style.Theme_Material_Light_NoActionBar);
        current = dialog;
        LinearLayout layout = new LinearLayout(activity);
        layout.setOrientation(LinearLayout.VERTICAL);
        Button close = new Button(activity);
        close.setText("返回 Meerkat");
        close.setOnClickListener(v -> dialog.dismiss());
        layout.addView(close);
        WebView page = new WebView(activity);
        currentPage = page;
        page.getSettings().setJavaScriptEnabled(true);
        page.getSettings().setDomStorageEnabled(true);
        page.getSettings().setAllowFileAccess(false);
        page.getSettings().setAllowContentAccess(false);
        page.setWebViewClient(new WebViewClient() {
            private boolean allowed(Uri url) {
                return entry.getScheme().equals(url.getScheme()) && entry.getEncodedAuthority().equals(url.getEncodedAuthority());
            }
            @Override public boolean shouldOverrideUrlLoading(WebView view, String url) { return !allowed(Uri.parse(url)); }
            @Override public boolean shouldOverrideUrlLoading(WebView view, WebResourceRequest req) { return !allowed(req.getUrl()); }
            @Override public WebResourceResponse shouldInterceptRequest(WebView view, WebResourceRequest req) {
                if (allowed(req.getUrl())) return null;
                return new WebResourceResponse("text/plain", "utf-8", 403, "Forbidden", Collections.emptyMap(), new ByteArrayInputStream(new byte[0]));
            }
        });
        layout.addView(page, ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.MATCH_PARENT);
        dialog.setContentView(layout);
        dialog.setOnDismissListener(d -> {
            if (currentID == id) dismissCurrent();
        });
        dialog.show();
        page.loadUrl(entry.toString());
    }
}
