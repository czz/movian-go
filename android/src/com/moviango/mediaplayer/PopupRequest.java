package com.moviango.mediaplayer;

import java.util.concurrent.CountDownLatch;
import java.util.concurrent.TimeUnit;

/**
 * One user-mediated popup at a time. Plain Java (no Android types) so the
 * state machine is testable with javac alone.
 *
 * The caller thread waits in two phases: the popup Activity must claim the
 * request within the launch timeout (Android may silently drop an activity
 * start), then the user has the whole session limit to finish the page. The
 * launch timeout never shortens the time the user spends on the page.
 *
 * WebPopupActivity drives it: the Activity polls isDone() and closes itself;
 * the cookie query waits at most COOKIE_QUERY_TIMEOUT_MS; the Go caller waits
 * longer than LAUNCH_TIMEOUT_MS + SESSION_TIMEOUT_MS; Back, onDestroy and
 * renderer loss finish the request instead of leaving it open.
 *
 * A request may carry a wait timeout (the plugin's waitForCookie limit). When
 * it runs out the result is "timeout", from await() or from the Activity's
 * pollTimeout(), whichever notices first.
 */
final class PopupRequest {
    /** Time for the popup Activity to reach onCreate and claim the request. */
    static final long LAUNCH_TIMEOUT_MS = 15_000L;
    /** Upper bound for one user session in the popup. */
    static final long SESSION_TIMEOUT_MS = 30L * 60L * 1000L;
    /** Upper bound for the cookie query that runs on the UI thread. */
    static final long COOKIE_QUERY_TIMEOUT_MS = 10_000L;
    /** Upper bound for deleting cookies on the UI thread; below the Go side's 10 s. */
    static final long COOKIE_DELETE_TIMEOUT_MS = 8_000L;

    private static final Object LOCK = new Object();
    private static PopupRequest active;

    final String uri, title, trap, waitUrl, waitName, owner;
    /** Cookie wait limit in ms; 0 means only the session limit applies. */
    final long waitTimeoutMs;

    private final CountDownLatch done = new CountDownLatch(1);
    private boolean claimed;          // guarded by LOCK
    private long claimedAtNanos;      // guarded by LOCK
    private volatile String result;   // written once under LOCK

    private PopupRequest(String uri, String title, String trap,
                         String waitUrl, String waitName, String owner,
                         long waitTimeoutMs) {
        this.waitTimeoutMs = Math.max(0L, waitTimeoutMs);
        this.uri = uri;
        this.title = title == null ? "" : title;
        this.trap = trap == null ? "" : trap;
        this.waitUrl = waitUrl == null ? "" : waitUrl;
        this.waitName = waitName == null ? "" : waitName;
        this.owner = owner;
    }

    static PopupRequest begin(String uri, String title, String trap,
                              String waitUrl, String waitName, String owner) {
        return begin(uri, title, trap, waitUrl, waitName, owner, 0L);
    }

    /** Registers a new active request, or returns null if one is active. */
    static PopupRequest begin(String uri, String title, String trap,
                              String waitUrl, String waitName, String owner,
                              long waitTimeoutMs) {
        synchronized (LOCK) {
            if (active != null) return null;
            active = new PopupRequest(uri, title, trap, waitUrl, waitName, owner,
                    waitTimeoutMs);
            return active;
        }
    }

    /**
     * Called by the popup Activity's onCreate. Returns the active request if
     * it is still unfinished and unclaimed; otherwise null and the Activity
     * must finish itself.
     */
    static PopupRequest claim() {
        synchronized (LOCK) {
            PopupRequest r = active;
            // finish() clears active together with setting the result.
            if (r == null || r.claimed) return null;
            r.claimed = true;
            r.claimedAtNanos = System.nanoTime();
            LOCK.notifyAll();
            return r;
        }
    }

    /**
     * Sets the result once and releases the active slot. Returns false if a
     * result was already set; the first result always wins.
     */
    boolean finish(String json) {
        synchronized (LOCK) {
            if (result != null) return false;
            finishLocked(json == null ? json("neterror", null, null) : json);
            return true;
        }
    }

    /**
     * The user pressed Back. topUrl is the page the top frame showed; its
     * origin is reported when it is https (the Go side still checks it against
     * the cookie target), otherwise the result carries no origin.
     */
    boolean finishBack(String topUrl) { return finish(json("userclose", null, httpsOriginOf(topUrl))); }

    /** The Activity is being destroyed; no-op when a result already exists. */
    boolean finishDestroyed(String topUrl) { return finish(json("userclose", null, httpsOriginOf(topUrl))); }

    /** originOf(value) for an https URL, else null. */
    static String httpsOriginOf(String value) {
        if (value == null || !value.regionMatches(true, 0, "https://", 0, 8)) return null;
        return originOf(value);
    }

    /** The WebView renderer process died. */
    boolean finishRendererGone() { return finish(json("neterror", null, null)); }

    /**
     * Called from the Activity's poll. Finishes the request with "timeout"
     * once the wait limit, counted from the claim, has passed. Returns true
     * if this call finished it.
     */
    boolean pollTimeout() {
        if (waitTimeoutMs <= 0) return false;
        synchronized (LOCK) {
            if (!claimed || result != null) return false;
            if (System.nanoTime() - claimedAtNanos < TimeUnit.MILLISECONDS.toNanos(waitTimeoutMs))
                return false;
        }
        return finish(json("timeout", null, null));
    }

    /** True if url is a navigation to the trap (callback) URL. */
    boolean matchesTrap(String url) {
        return !trap.isEmpty() && url != null && url.startsWith(trap);
    }

    /**
     * The URL to query for the awaited cookie, or null when the cookie wait
     * does not apply now. The cookie is queried for waitUrl whatever path the
     * page shows (a challenge at /challenge may set a root cookie needed for
     * /api), but only while the top frame is on the opener's origin, and only
     * for an https waitUrl on that same origin.
     */
    String cookieQueryUrl(String topFrameUrl) {
        if (waitUrl.isEmpty() || waitName.isEmpty() || topFrameUrl == null) return null;
        if (!waitUrl.regionMatches(true, 0, "https://", 0, 8)) return null;
        String source = originOf(uri);
        if (source == null) return null;
        if (!source.equals(originOf(topFrameUrl))) return null;
        return source.equals(originOf(waitUrl)) ? waitUrl : null;
    }

    /** scheme://host[:port], lower-cased; null for anything unparsable. */
    static String originOf(String value) {
        if (value == null) return null;
        try {
            java.net.URI u = new java.net.URI(value);
            String scheme = u.getScheme(), host = u.getHost();
            if (scheme == null || host == null) return null;
            int port = u.getPort();
            scheme = scheme.toLowerCase(java.util.Locale.US);
            // The default port is the same origin as no port.
            if ((port == 443 && scheme.equals("https")) || (port == 80 && scheme.equals("http")))
                port = -1;
            return scheme + "://" + host.toLowerCase(java.util.Locale.US)
                    + (port >= 0 ? ":" + port : "");
        } catch (Exception e) { return null; }
    }

    /** True if the Cookie-header style string holds name with a non-empty value. */
    static boolean hasCookie(String cookies, String name) {
        if (cookies == null) return false;
        String p = name + "=";
        for (String part : cookies.split(";")) {
            String pair = part.trim();
            if (pair.startsWith(p) && pair.length() > p.length()) return true;
        }
        return false;
    }

    /** True once a result is set; the Activity polls this to close itself. */
    boolean isDone() {
        return result != null;
    }

    /**
     * Blocks the calling (non-UI) thread until the request finishes. An
     * unclaimed request fails after launchMs; a claimed one after sessionMs
     * counted from the start of the call. Interruption fails the request and
     * preserves the interrupt flag.
     */
    String await(long launchMs, long sessionMs) {
        long start = System.nanoTime();
        long sessionDeadline = start + TimeUnit.MILLISECONDS.toNanos(sessionMs);
        long launchDeadline = start + TimeUnit.MILLISECONDS.toNanos(Math.min(launchMs, sessionMs));
        try {
            synchronized (LOCK) {
                while (result == null) {
                    long deadline = launchDeadline;
                    boolean waitLimited = false;
                    if (claimed) {
                        // The cookie-wait allowance starts at the claim, so a
                        // slow Activity start never eats the user's time.
                        deadline = sessionDeadline;
                        if (waitTimeoutMs > 0) {
                            long wd = claimedAtNanos + TimeUnit.MILLISECONDS.toNanos(waitTimeoutMs);
                            if (wd - sessionDeadline <= 0) { deadline = wd; waitLimited = true; }
                        }
                    }
                    long left = deadline - System.nanoTime();
                    if (left <= 0) {
                        // An unclaimed request fails here; a late onCreate then
                        // finds a result and gets null.
                        finishLocked(json(waitLimited ? "timeout" : "neterror", null, null));
                        break;
                    }
                    TimeUnit.NANOSECONDS.timedWait(LOCK, left);
                }
            }
        } catch (InterruptedException e) {
            Thread.currentThread().interrupt();
        }
        finish(json("neterror", null, null)); // no-op if already finished
        return result;
    }

    private void finishLocked(String json) {
        if (result != null) return;
        result = json;
        if (active == this) active = null;
        done.countDown();
        LOCK.notifyAll();
    }

    /** Builds the result object returned through JNI. */
    static String json(String result, String url, String topOrigin) {
        StringBuilder b = new StringBuilder(64).append("{\"result\":");
        quote(b, result == null ? "neterror" : result);
        if (url != null) quote(b.append(",\"url\":"), url);
        if (topOrigin != null) quote(b.append(",\"topOrigin\":"), topOrigin);
        return b.append('}').toString();
    }

    private static void quote(StringBuilder b, String s) {
        b.append('"');
        for (int i = 0; i < s.length(); i++) {
            char c = s.charAt(i);
            switch (c) {
                case '"': b.append("\\\""); break;
                case '\\': b.append("\\\\"); break;
                case '\n': b.append("\\n"); break;
                case '\r': b.append("\\r"); break;
                case '\t': b.append("\\t"); break;
                default:
                    if (c < 0x20 || c == '\u2028' || c == '\u2029')
                        b.append(String.format("\\u%04x", (int) c));
                    else
                        b.append(c);
            }
        }
        b.append('"');
    }
}
