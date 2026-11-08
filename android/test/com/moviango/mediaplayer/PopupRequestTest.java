package com.moviango.mediaplayer;

import java.util.concurrent.CountDownLatch;
import java.util.concurrent.atomic.AtomicInteger;
import java.util.concurrent.atomic.AtomicReference;

/**
 * Standalone checks for PopupRequest; no Android runtime or JUnit:
 *   javac -d OUT android/src/com/moviango/mediaplayer/PopupRequest.java \
 *       android/test/com/moviango/mediaplayer/PopupRequestTest.java
 *   java -cp OUT com.moviango.mediaplayer.PopupRequestTest
 */
public final class PopupRequestTest {
    private static final String NETERROR = "{\"result\":\"neterror\"}";

    public static void main(String[] args) throws Exception {
        singleActive();
        finishOnce();
        launchTimeoutFailsUnclaimed();
        launchTimeoutDoesNotLimitUser();
        sessionTimeoutFailsClaimed();
        claimOnce();
        interruptFailsAndKeepsFlag();
        startFailureClearsActive();
        racingFinishersOneWins();
        claimRacesLaunchTimeout();
        jsonEscapes();
        backFinishesRequest();
        destroyFinishesOpenRequest();
        destroyAfterResultKeepsResult();
        rendererLossFinishesRequest();
        cookieWaitTimeoutResult();
        pollTimeoutMatchesAwait();
        noWaitTimeoutKeepsNeterror();
        waitLimitCountsFromClaim();
        trapMatching();
        trapResultNamesDisplayedOrigin();
        cookieQueryUrlIgnoresDisplayedPath();
        cookieQueryUrlKeepsOriginChecks();
        hasCookieChecks();
        defaultPortIsSameOrigin();
        userCloseReportsHttpsTopOrigin();
        System.out.println("PopupRequestTest: all checks passed");
    }

    private static PopupRequest begin() {
        PopupRequest r = PopupRequest.begin("https://example.test/", "t", null, null, null, "plugin.a");
        require(r != null, "begin returned null while idle");
        return r;
    }

    static void singleActive() {
        PopupRequest r = begin();
        require(PopupRequest.begin("https://example.test/", "", "", "", "", "plugin.b") == null,
                "second request accepted while one is active");
        require(r.finish(PopupRequest.json("userclose", null, null)), "finish failed");
        PopupRequest next = begin();
        next.finish(null);
    }

    static void finishOnce() {
        PopupRequest r = begin();
        String first = PopupRequest.json("cookie", "https://example.test/", "https://example.test");
        require(r.finish(first), "first finish rejected");
        require(!r.finish(PopupRequest.json("userclose", null, null)), "second finish accepted");
        require(r.await(10, 10).equals(first), "result changed after first finish");
        require(r.isDone(), "isDone false after finish");
    }

    static void launchTimeoutFailsUnclaimed() {
        PopupRequest r = begin();
        long t0 = System.nanoTime();
        String out = r.await(50, 60_000);
        long ms = (System.nanoTime() - t0) / 1_000_000;
        require(out.equals(NETERROR), "unclaimed request: " + out);
        require(ms < 5_000, "launch timeout waited for the session limit: " + ms + "ms");
        require(PopupRequest.claim() == null, "late onCreate claimed a timed-out request");
        begin().finish(null); // active slot was released
    }

    static void launchTimeoutDoesNotLimitUser() throws Exception {
        final PopupRequest r = begin();
        require(PopupRequest.claim() == r, "claim did not return the active request");
        final String user = PopupRequest.json("cookie", "https://example.test/", "https://example.test");
        Thread activity = new Thread(new Runnable() {
            @Override public void run() {
                sleep(400); // the user works far past the 50ms launch timeout
                r.finish(user);
            }
        });
        activity.start();
        String out = r.await(50, 60_000);
        activity.join();
        require(out.equals(user), "claimed request cut short by launch timeout: " + out);
    }

    static void sessionTimeoutFailsClaimed() {
        PopupRequest r = begin();
        require(PopupRequest.claim() == r, "claim failed");
        long t0 = System.nanoTime();
        String out = r.await(20, 200);
        long ms = (System.nanoTime() - t0) / 1_000_000;
        require(out.equals(NETERROR), "session timeout result: " + out);
        require(ms >= 150, "session limit ended early: " + ms + "ms");
        require(r.isDone(), "Activity cannot observe the session timeout");
        require(!r.finish(PopupRequest.json("cookie", null, null)), "finish after session timeout");
        begin().finish(null);
    }

    static void claimOnce() {
        PopupRequest r = begin();
        require(PopupRequest.claim() == r, "first claim failed");
        require(PopupRequest.claim() == null, "request claimed twice");
        r.finish(null);
        require(PopupRequest.claim() == null, "finished request claimed");
    }

    static void interruptFailsAndKeepsFlag() throws Exception {
        final PopupRequest r = begin();
        PopupRequest.claim();
        final AtomicReference<String> out = new AtomicReference<String>();
        final AtomicReference<Boolean> flag = new AtomicReference<Boolean>();
        Thread caller = new Thread(new Runnable() {
            @Override public void run() {
                out.set(r.await(10, 60_000));
                flag.set(Thread.currentThread().isInterrupted());
            }
        });
        caller.start();
        sleep(100);
        caller.interrupt();
        caller.join(5_000);
        require(!caller.isAlive(), "interrupted await did not return");
        require(NETERROR.equals(out.get()), "interrupt result: " + out.get());
        require(Boolean.TRUE.equals(flag.get()), "interrupt flag lost");
        begin().finish(null);
    }

    static void startFailureClearsActive() {
        // startActivity threw: the opener fails the request itself.
        PopupRequest r = begin();
        require(r.finish(null), "error finish rejected");
        require(r.await(10, 10).equals(NETERROR), "error result");
        begin().finish(null);
    }

    static void racingFinishersOneWins() throws Exception {
        for (int round = 0; round < 200; round++) {
            final PopupRequest r = begin();
            final CountDownLatch go = new CountDownLatch(1);
            final AtomicInteger wins = new AtomicInteger();
            Thread[] ts = new Thread[8];
            for (int i = 0; i < ts.length; i++) {
                final String res = PopupRequest.json("r" + i, null, null);
                ts[i] = new Thread(new Runnable() {
                    @Override public void run() {
                        awaitQuietly(go);
                        if (r.finish(res)) wins.incrementAndGet();
                    }
                });
                ts[i].start();
            }
            go.countDown();
            for (Thread t : ts) t.join();
            require(wins.get() == 1, "finishers won: " + wins.get());
            require(r.await(10, 10) != null, "no result");
        }
        begin().finish(null);
    }

    static void claimRacesLaunchTimeout() throws Exception {
        // If onCreate wins the claim, the launch timeout must not fail the
        // request under it: the Activity's own finish has to succeed.
        int claimedRounds = 0;
        for (int round = 0; round < 500; round++) {
            final PopupRequest r = begin();
            final AtomicReference<Boolean> activityFinished = new AtomicReference<Boolean>();
            final long delay = round % 3; // 0..2ms around the 1ms launch timeout
            Thread activity = new Thread(new Runnable() {
                @Override public void run() {
                    sleep(delay);
                    PopupRequest mine = PopupRequest.claim();
                    if (mine != null) activityFinished.set(mine.finish(PopupRequest.json("userclose", null, null)));
                }
            });
            activity.start();
            String out = r.await(1, 2_000);
            activity.join();
            if (activityFinished.get() != null) {
                claimedRounds++;
                require(activityFinished.get(), "launch timeout failed a claimed request");
                require(out.equals("{\"result\":\"userclose\"}"), "claimed round result: " + out);
            } else {
                require(out.equals(NETERROR), "unclaimed round result: " + out);
            }
            require(PopupRequest.claim() == null, "active slot leaked");
        }
        System.out.println("claimRacesLaunchTimeout: claimed " + claimedRounds + "/500 rounds");
    }

    static void jsonEscapes() {
        String s = PopupRequest.json("cookie", "https://e.test/a\"b\\c\td\u0001e\u2028f", null);
        require(s.equals("{\"result\":\"cookie\",\"url\":\"https://e.test/a\\\"b\\\\c\\td\\u0001e\\u2028f\"}"), s);
        require(PopupRequest.json(null, null, null).equals(NETERROR), "null result");
        require(PopupRequest.json("trapped", null, "https://e.test")
                .equals("{\"result\":\"trapped\",\"topOrigin\":\"https://e.test\"}"), "topOrigin");
    }

    static void backFinishesRequest() {
        PopupRequest r = begin();
        require(PopupRequest.claim() == r, "claim");
        require(r.finishBack(null), "Back did not finish");
        require(r.isDone(), "isDone false after Back");
        require(r.await(10, 10).equals("{\"result\":\"userclose\"}"), "Back result");
        begin().finish(null); // slot released
    }

    static void destroyFinishesOpenRequest() {
        PopupRequest r = begin();
        PopupRequest.claim();
        require(r.finishDestroyed(null), "destroy did not finish");
        require(r.await(10, 10).equals("{\"result\":\"userclose\"}"), "destroy result");
        begin().finish(null);
    }

    static void destroyAfterResultKeepsResult() {
        PopupRequest r = begin();
        PopupRequest.claim();
        String cookie = PopupRequest.json("cookie", "https://example.test/", "https://example.test");
        r.finish(cookie);
        require(!r.finishDestroyed(null), "destroy overrode a finished request");
        require(r.await(10, 10).equals(cookie), "result changed by destroy");
    }

    static void rendererLossFinishesRequest() {
        PopupRequest r = begin();
        PopupRequest.claim();
        require(r.finishRendererGone(), "renderer loss did not finish");
        require(r.await(10, 10).equals(NETERROR), "renderer loss result");
        begin().finish(null);
    }

    static void cookieWaitTimeoutResult() {
        PopupRequest r = PopupRequest.begin("https://example.test/", "t", null,
                "https://example.test/", "cf_clearance", "plugin.a", 80);
        require(r != null, "begin with wait timeout");
        PopupRequest.claim();
        long t0 = System.nanoTime();
        String out = r.await(1_000, 60_000);
        long ms = (System.nanoTime() - t0) / 1_000_000;
        require(out.equals("{\"result\":\"timeout\"}"), "wait timeout result: " + out);
        require(ms < 5_000, "wait timeout ignored: " + ms + "ms");
        begin().finish(null);
    }

    static void pollTimeoutMatchesAwait() {
        PopupRequest r = PopupRequest.begin("https://example.test/", "t", null,
                "https://example.test/", "cf_clearance", "plugin.a", 60);
        PopupRequest.claim();
        require(!r.pollTimeout(), "pollTimeout fired early");
        sleep(120);
        require(r.pollTimeout(), "pollTimeout did not fire");
        require(r.isDone(), "isDone false after poll timeout");
        require(r.await(10, 10).equals("{\"result\":\"timeout\"}"), "poll timeout result");
        begin().finish(null);
    }

    static void noWaitTimeoutKeepsNeterror() {
        PopupRequest r = begin();
        PopupRequest.claim();
        require(!r.pollTimeout(), "pollTimeout without a wait limit");
        require(r.await(1_000, 60).equals(NETERROR), "session expiry without wait limit");
        begin().finish(null);
    }

    static void waitLimitCountsFromClaim() throws Exception {
        PopupRequest r = PopupRequest.begin("https://example.test/", "t", null,
                "https://example.test/", "cf_clearance", "plugin.a", 600);
        Thread activity = new Thread(() -> {
            sleep(400); // slow Activity start
            PopupRequest.claim();
            sleep(350); // inside the 600 ms allowance counted from the claim
            r.finish(PopupRequest.json("cookie", "https://example.test/", "https://example.test"));
        });
        activity.start();
        String out = r.await(5_000, 60_000);
        activity.join();
        require(out.startsWith("{\"result\":\"cookie\""), "slow start ate the wait allowance: " + out);
        begin().finish(null);
    }

    private static PopupRequest req(String uri, String trap, String waitUrl, String waitName) {
        PopupRequest r = PopupRequest.begin(uri, "t", trap, waitUrl, waitName, "plugin.a");
        require(r != null, "begin");
        return r;
    }

    /**
     * A trapped navigation is intercepted and never loaded: the result keeps
     * the trapped URL but its topOrigin is the origin the top frame actually
     * shows, https only. A cross-origin trap must not name the trap's origin.
     */
    static void trapResultNamesDisplayedOrigin() {
        String trapped = "https://target.example/callback?code=1";
        // Cross-origin trap: the page shown is other.example.
        String s = PopupRequest.resultJson("trapped", trapped, "https://other.example/login");
        require(s.equals("{\"result\":\"trapped\",\"url\":\"https://target.example/callback?code=1\","
                + "\"topOrigin\":\"https://other.example\"}"), "cross-origin trap: " + s);
        require(!s.contains("\"topOrigin\":\"https://target.example\""), "trap origin leaked: " + s);
        // Same-origin trap still names that origin.
        s = PopupRequest.resultJson("trapped", "https://other.example/done", "https://other.example/login");
        require(s.contains("\"topOrigin\":\"https://other.example\""), "same-origin trap: " + s);
        // Nothing displayed yet, or a page that is not https: no origin.
        s = PopupRequest.resultJson("trapped", trapped, null);
        require(s.equals("{\"result\":\"trapped\",\"url\":\"https://target.example/callback?code=1\"}"), "no page: " + s);
        s = PopupRequest.resultJson("trapped", trapped, "http://other.example/");
        require(!s.contains("topOrigin"), "plain http page: " + s);
        // A found cookie names the displayed page too.
        s = PopupRequest.resultJson("cookie", "https://a.example/x", "https://a.example/x");
        require(s.contains("\"topOrigin\":\"https://a.example\""), "cookie: " + s);
    }

    static void trapMatching() {
        PopupRequest r = req("https://e.test/login", "https://e.test/done", "", "");
        require(r.matchesTrap("https://e.test/done?code=1"), "trap prefix");
        require(!r.matchesTrap("https://e.test/other"), "trap mismatch");
        require(!r.matchesTrap(null), "null url");
        r.finish(null);
        PopupRequest none = req("https://e.test/login", "", "", "");
        require(!none.matchesTrap("https://e.test/done"), "empty trap must match nothing");
        none.finish(null);
    }

    // A challenge page at /challenge sets a root-path cookie needed for /api:
    // the cookie must be queried for the wait URL while the page shows /challenge.
    static void cookieQueryUrlIgnoresDisplayedPath() {
        PopupRequest r = req("https://e.test/challenge", "", "https://e.test/api", "cf_clearance");
        require("https://e.test/api".equals(r.cookieQueryUrl("https://e.test/challenge")),
                "wait URL not queried while another path is displayed");
        r.finish(null);
    }

    static void cookieQueryUrlKeepsOriginChecks() {
        PopupRequest r = req("https://e.test/challenge", "", "https://e.test/api", "cf_clearance");
        require(r.cookieQueryUrl("https://evil.test/challenge") == null, "foreign top frame");
        require(r.cookieQueryUrl("http://e.test/challenge") == null, "downgraded top frame");
        require(r.cookieQueryUrl(null) == null, "null top frame");
        r.finish(null);
        PopupRequest plain = req("http://e.test/c", "", "http://e.test/api", "n");
        require(plain.cookieQueryUrl("http://e.test/c") == null, "http wait URL queried");
        plain.finish(null);
        PopupRequest other = req("https://e.test/c", "", "https://other.test/api", "n");
        require(other.cookieQueryUrl("https://e.test/c") == null, "wait URL on another origin");
        other.finish(null);
        PopupRequest noName = req("https://e.test/c", "", "https://e.test/api", "");
        require(noName.cookieQueryUrl("https://e.test/c") == null, "no cookie name");
        noName.finish(null);
        PopupRequest noUrl = req("https://e.test/c", "", "", "n");
        require(noUrl.cookieQueryUrl("https://e.test/c") == null, "no wait URL");
        noUrl.finish(null);
    }

    static void hasCookieChecks() {
        require(PopupRequest.hasCookie("a=1; cf_clearance=xyz; b=2", "cf_clearance"), "present");
        require(!PopupRequest.hasCookie("cf_clearance=; a=1", "cf_clearance"), "empty value");
        require(!PopupRequest.hasCookie("xcf_clearance=1", "cf_clearance"), "name suffix");
        require(!PopupRequest.hasCookie(null, "cf_clearance"), "null");
    }

    static void defaultPortIsSameOrigin() {
        PopupRequest r = req("https://e.test:443/challenge", "", "https://e.test:443/api", "n");
        require("https://e.test:443/api".equals(r.cookieQueryUrl("https://e.test/challenge")),
                "explicit :443 treated as another origin");
        require(r.cookieQueryUrl("https://e.test:8443/challenge") == null, "other port accepted");
        r.finish(null);
    }

    static void userCloseReportsHttpsTopOrigin() {
        PopupRequest r = begin();
        PopupRequest.claim();
        require(r.finishBack("https://Forum.Example:443/login?x=1"), "Back did not finish");
        require(r.await(10, 10).equals("{\"result\":\"userclose\",\"topOrigin\":\"https://forum.example\"}"),
                "Back must report the https top origin");

        r = begin();
        PopupRequest.claim();
        require(r.finishDestroyed("https://forum.example:8443/"), "destroy did not finish");
        require(r.await(10, 10).equals("{\"result\":\"userclose\",\"topOrigin\":\"https://forum.example:8443\"}"),
                "destroy must report the https top origin with its port");

        for (String bad : new String[] {null, "", "http://forum.example/", "about:blank", "file:///x", "data:text/html,x", "not a url"}) {
            r = begin();
            PopupRequest.claim();
            r.finishBack(bad);
            require(r.await(10, 10).equals("{\"result\":\"userclose\"}"), "no origin expected for " + bad);
        }
    }

    private static void sleep(long ms) {
        try { Thread.sleep(ms); } catch (InterruptedException e) { Thread.currentThread().interrupt(); }
    }

    private static void awaitQuietly(CountDownLatch l) {
        try { l.await(); } catch (InterruptedException e) { Thread.currentThread().interrupt(); }
    }

    private static void require(boolean condition, String message) {
        if (!condition) throw new AssertionError(message);
    }
}
