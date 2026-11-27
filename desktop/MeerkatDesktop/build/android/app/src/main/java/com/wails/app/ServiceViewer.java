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
import org.json.JSONObject;

/** Untrusted service documents have a separate WebView with no native interfaces. */
final class ServiceViewer {
    static void attach(Activity activity, WebView shell, WailsBridge bridge, String shellOrigin) {
        if (!WebViewFeature.isFeatureSupported(WebViewFeature.WEB_MESSAGE_LISTENER)) return;
        WebViewCompat.addWebMessageListener(shell, "meerkatServiceViewer", Collections.singleton(shellOrigin),
                (view, message, origin, mainFrame, reply) -> {
                    if (!mainFrame || !Uri.parse(shellOrigin).equals(origin) || message.getType() != WebMessageCompat.TYPE_STRING) return;
                    String address = message.getData();
                    if (address == null || address.length() > 4096) return;
                    try {
                        byte[] response = bridge.serveAsset("/client/service/open?url=" + Uri.encode(address), "POST", "{}");
                        JSONObject result = new JSONObject(new String(response, StandardCharsets.UTF_8));
                        if (result.has("url")) {
                            show(activity, bridge, Uri.parse(result.getString("url")));
                            reply.postMessage(new JSONObject().put("saved", address).toString());
                        } else reply.postMessage(result.toString());
                    } catch (Exception err) {
                        reply.postMessage("{\"error\":\"无法打开服务，请检查内嵌网络后重试。\"}");
                    }
                });
    }

    private static void show(Activity activity, WailsBridge bridge, Uri entry) {
        Dialog dialog = new Dialog(activity, android.R.style.Theme_Material_Light_NoActionBar);
        LinearLayout layout = new LinearLayout(activity);
        layout.setOrientation(LinearLayout.VERTICAL);
        Button close = new Button(activity);
        close.setText("返回 Meerkat");
        close.setOnClickListener(v -> dialog.dismiss());
        layout.addView(close);
        WebView page = new WebView(activity);
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
            page.stopLoading();
            layout.removeView(page);
            page.destroy();
            bridge.serveAsset("/client/service/close", "POST", "{}");
        });
        dialog.show();
        page.loadUrl(entry.toString());
    }
}
