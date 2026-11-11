package com.moviango.mediaplayer;

/**
 * Standalone checks for OneShot; no Android runtime or JUnit:
 *   javac -d OUT android/src/com/moviango/mediaplayer/OneShot.java \
 *       android/test/com/moviango/mediaplayer/OneShotTest.java
 *   java -cp OUT com.moviango.mediaplayer.OneShotTest
 */
public final class OneShotTest {
    public static void main(String[] args) {
        repeatedResumesDeliverOnce();
        laterIntentIsDeliveredOnce();
        latestIntentBeforeResumeWins();
        intentWithoutUriReplacesPending();
        System.out.println("OneShotTest: all checks passed");
    }

    // Returning from the popup resumes GLWActivity again: the launch URI must
    // not be delivered a second time (it would reopen the route and relaunch
    // the popup).
    static void repeatedResumesDeliverOnce() {
        OneShot<String> o = new OneShot<>();
        o.offer("movian:plugin:a");
        require("movian:plugin:a".equals(o.take()), "first resume");
        require(o.take() == null, "second resume redelivered the URI");
        require(o.take() == null, "third resume redelivered the URI");
    }

    static void laterIntentIsDeliveredOnce() {
        OneShot<String> o = new OneShot<>();
        o.offer("movian:plugin:a");
        o.take();
        o.take();
        o.offer("movian:plugin:b"); // onNewIntent
        require("movian:plugin:b".equals(o.take()), "new intent lost");
        require(o.take() == null, "new intent redelivered");
    }

    static void latestIntentBeforeResumeWins() {
        OneShot<String> o = new OneShot<>();
        o.offer("a");
        o.offer("b");
        require("b".equals(o.take()), "latest intent did not win");
        require(o.take() == null, "redelivered");
    }

    static void intentWithoutUriReplacesPending() {
        OneShot<String> o = new OneShot<>();
        o.offer("a");
        o.offer(null);
        require(o.take() == null, "stale URI survived an intent without one");
    }

    private static void require(boolean condition, String message) {
        if (!condition) throw new AssertionError(message);
    }
}
