package com.moviango.mediaplayer;

import android.app.Activity;
import android.os.Bundle;
import android.os.Handler;
import android.os.Looper;
import android.view.KeyEvent;
import android.view.View;
import android.webkit.CookieManager;
import android.webkit.WebResourceError;
import android.webkit.WebResourceRequest;
import android.webkit.WebSettings;
import android.webkit.WebView;
import android.webkit.RenderProcessGoneDetail;
import android.webkit.WebViewClient;
import android.widget.FrameLayout;

import androidx.webkit.CookieManagerCompat;
import androidx.webkit.Profile;
import androidx.webkit.ProfileStore;
import androidx.webkit.WebViewCompat;
import androidx.webkit.WebViewFeature;


import java.util.List;
import java.util.concurrent.CountDownLatch;
import java.util.concurrent.TimeUnit;

/** User-mediated, clearance-only WebView. No JavaScript bridge is installed. */
public final class WebPopupActivity extends Activity {
    private static volatile String lastUserAgent = "";

    private WebView webView;
    private CookieManager profileCookies;
    private PopupRequest request;
    private final Handler handler = new Handler(Looper.getMainLooper());
    private final Runnable waitCookie = new Runnable() {
        @Override public void run() {
            if (request == null || finishingResult || webView == null) return;
            // The caller gave up (launch/session limit) or the wait limit ran out.
            if (request.isDone()) { closeFinished(); return; }
            if (request.pollTimeout()) { closeFinished(); return; }
            checkCompletion(webView.getUrl());
            if (!finishingResult) handler.postDelayed(this, 500);
        }
    };
    private boolean finishingResult;

    /**
     * Blocks the calling (non-UI) thread. Bounded: 15 s for the Activity to
     * claim the request, then at most waitTimeoutMs (or 30 min when 0).
     */
    static String open(String uri, String title, String trap, String waitUrl,
                       String waitName, String pluginOwner, long waitTimeoutMs,
                       Activity owner) {
        if (Looper.myLooper() == Looper.getMainLooper())
            return neterror();
        if (uri == null || !(uri.startsWith("https://") || uri.startsWith("http://")))
            return neterror();
        final PopupRequest r = PopupRequest.begin(uri, title, trap, waitUrl,
                waitName, pluginOwner, waitTimeoutMs);
        if (r == null) return neterror();
        lastUserAgent = "";
        owner.runOnUiThread(new Runnable() {
            @Override public void run() {
                try {
                    android.content.Intent i = new android.content.Intent(owner, WebPopupActivity.class);
                    owner.startActivity(i);
                } catch (Throwable t) {
                    r.finish(neterror());
                }
            }
        });
        return r.await(PopupRequest.LAUNCH_TIMEOUT_MS, PopupRequest.SESSION_TIMEOUT_MS);
    }

    private static String neterror() {
        return PopupRequest.json("neterror", null, null);
    }

    private static String originOf(String value) {
        if (value == null) return null;
        try {
            android.net.Uri u = android.net.Uri.parse(value);
            String scheme = u.getScheme(), host = u.getHost();
            if (scheme == null || host == null) return null;
            int port = u.getPort();
            return scheme.toLowerCase(java.util.Locale.US) + "://" + host.toLowerCase(java.util.Locale.US)
                    + (port >= 0 ? ":" + port : "");
        } catch (Throwable ignored) { return null; }
    }
    private static String profileName(String owner) {
        try {
            byte[] hash = java.security.MessageDigest.getInstance("SHA-256")
                .digest(owner.getBytes(java.nio.charset.StandardCharsets.UTF_8));
            StringBuilder b = new StringBuilder("clearance-");
            for (byte v : hash) b.append(String.format(java.util.Locale.US, "%02x", v & 0xff));
            return b.toString();
        } catch (Exception e) { return null; }
    }

    @Override protected void onCreate(Bundle state) {
        super.onCreate(state);
        request = PopupRequest.claim();
        if (request == null) { finish(); return; }
        // A missing or disabled WebView provider throws from the feature check
        // and from the WebView constructor; fail the request, never the app.
        try {
            if (!WebViewFeature.isFeatureSupported(WebViewFeature.MULTI_PROFILE) ||
                !WebViewFeature.isFeatureSupported(WebViewFeature.GET_COOKIE_INFO)) {
                complete("neterror", null);
                return;
            }
            setTitle(request.title);
            webView = new WebView(this);
            String profile = profileName(request.owner);
            if (profile == null) { complete("neterror", null); return; }
            WebViewCompat.setProfile(webView, profile);
            profileCookies = WebViewCompat.getProfile(webView).getCookieManager();
        } catch (Throwable ignored) { complete("neterror", null); return; }
        webView.setFocusable(true);
        webView.setFocusableInTouchMode(true);
        webView.requestFocus(View.FOCUS_DOWN);
        WebSettings settings = webView.getSettings();
        lastUserAgent = settings.getUserAgentString();
        settings.setJavaScriptEnabled(true); // challenge pages only; no bridge or injected script
        settings.setDomStorageEnabled(true);
        webView.setWebViewClient(new WebViewClient() {
            @Override public boolean shouldOverrideUrlLoading(WebView v, WebResourceRequest r) {
                // Trap navigations complete here and are never loaded, so a
                // trap endpoint that fails or redirects cannot hide the match.
                return r.isForMainFrame() && trapNavigation(r.getUrl().toString());
            }
            @SuppressWarnings("deprecation")
            @Override public boolean shouldOverrideUrlLoading(WebView v, String url) {
                return trapNavigation(url);
            }
            @Override public void onPageFinished(WebView v, String url) {
                checkCompletion(url);
            }
            @Override public void onReceivedError(WebView v, WebResourceRequest r, WebResourceError e) {
                if (r.isForMainFrame() && (request == null
                        || !request.matchesTrap(r.getUrl().toString())))
                    complete("neterror", null);
            }
            @Override public boolean onRenderProcessGone(WebView v, RenderProcessGoneDetail detail) {
                // The renderer is gone: the WebView cannot be reused.
                if (request != null && !finishingResult) {
                    finishingResult = true;
                    request.finishRendererGone();
                    handler.removeCallbacks(waitCookie);
                    finish();
                }
                return true;
            }
        });
        setContentView(new FrameLayout(this) {{ addView(webView, new FrameLayout.LayoutParams(-1, -1)); }});
        webView.loadUrl(request.uri);
        handler.post(waitCookie);
    }

    private boolean trapNavigation(String url) {
        if (request == null || !request.matchesTrap(url)) return false;
        complete("trapped", url);
        return true;
    }

    private void checkCompletion(String url) {
        if (request == null) return;
        if (request.matchesTrap(url)) { complete("trapped", url); return; }
        // Query waitUrl, not the displayed URL: the cookie may be set for a
        // path other than the one the page shows.
        String queryUrl = request.cookieQueryUrl(url);
        if (queryUrl == null || profileCookies == null) return;
        try {
            String c = profileCookies.getCookie(queryUrl);
            if (PopupRequest.hasCookie(c, request.waitName)) complete("cookie", url);
        } catch (Throwable ignored) { }
    }
    private void complete(String result, String url) {
        if (finishingResult) return;
        finishingResult = true;
        if (url == null && webView != null) url = webView.getUrl();
        flushCookies();
        if (request != null)
            request.finish(PopupRequest.json(result, url, originOf(url)));
        handler.removeCallbacks(waitCookie);
        finish();
    }
    /** The URL the top frame shows, or null. */
    private String currentUrl() {
        try { return webView == null ? null : webView.getUrl(); } catch (Throwable t) { return null; }
    }
    private void flushCookies() {
        try { if (profileCookies != null) profileCookies.flush(); } catch (Throwable ignored) { }
    }
    /** The request already has a result (caller timed out): just close. */
    private void closeFinished() {
        if (finishingResult) return;
        finishingResult = true;
        handler.removeCallbacks(waitCookie);
        finish();
    }
    @Override public void onBackPressed() {
        if (finishingResult) return;
        finishingResult = true;
        flushCookies();
        if (request != null) request.finishBack(currentUrl());
        handler.removeCallbacks(waitCookie);
        finish();
    }
    @Override public boolean dispatchKeyEvent(KeyEvent e) {
        if (e.getKeyCode() == KeyEvent.KEYCODE_BACK && e.getAction() == KeyEvent.ACTION_DOWN) {
            onBackPressed(); return true;
        }
        return super.dispatchKeyEvent(e);
    }
    @Override protected void onDestroy() {
        // Finish the request even if the Activity dies without Back (system
        // kill, task removal): the caller must not wait for a dead window.
        handler.removeCallbacks(waitCookie);
        if (request != null) request.finishDestroyed(currentUrl());
        finishingResult = true;
        if (webView != null) {
            webView.stopLoading();
            if (webView.getParent() instanceof android.view.ViewGroup)
                ((android.view.ViewGroup) webView.getParent()).removeView(webView);
            webView.destroy();
            webView = null;
        }
        request = null;
        super.onDestroy();
    }

    static String cookies(String uri, String owner) {
        if (uri == null || owner == null || Looper.myLooper() == Looper.getMainLooper()) return null;
        final String[] out = new String[1]; // written before done.countDown()
        final CountDownLatch done = new CountDownLatch(1);
        PopupHost.runOnMain(new Runnable() { @Override public void run() {
            try {
                String name = profileName(owner);
                if (name == null || !WebViewFeature.isFeatureSupported(WebViewFeature.MULTI_PROFILE)) { done.countDown(); return; }
                Profile profile = ProfileStore.getInstance().getProfile(name);
                if (profile == null) { done.countDown(); return; }
                CookieManager cm = profile.getCookieManager();
                if (!WebViewFeature.isFeatureSupported(WebViewFeature.GET_COOKIE_INFO)) { out[0] = null; }
                else {
                    List<String> info = CookieManagerCompat.getCookieInfo(cm, uri);
                    out[0] = info == null ? null : new org.json.JSONArray(info).toString();
                }
            } catch (Throwable ignored) { out[0] = null; }
            done.countDown();
        }});
        try {
            if (!done.await(PopupRequest.COOKIE_QUERY_TIMEOUT_MS, TimeUnit.MILLISECONDS))
                return null;
        } catch (InterruptedException e) { Thread.currentThread().interrupt(); return null; }
        return out[0];
    }
    static String userAgent() {
        return lastUserAgent;
    }
}
