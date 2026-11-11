package com.moviango.mediaplayer;

/**
 * Holds the latest delivered value until it is taken once. Plain Java (no
 * Android types) so it is testable with javac alone.
 *
 * GLWActivity offers the URI of each delivered intent (launch and
 * onNewIntent) and takes it in onResume. Resuming again, for example when the
 * WebView popup closes, finds nothing, so the same route is not reopened and
 * the popup is not relaunched.
 */
final class OneShot<T> {
    private T value; // guarded by this

    /** Replaces any pending value; null means the new intent carried none. */
    synchronized void offer(T v) {
        value = v;
    }

    /** Returns the pending value once, then null until the next offer. */
    synchronized T take() {
        T v = value;
        value = null;
        return v;
    }
}
