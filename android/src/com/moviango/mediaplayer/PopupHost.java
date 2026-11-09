package com.moviango.mediaplayer;

import android.app.Activity;
import android.os.Handler;
import android.os.Looper;

/**
 * The Activity that hosts the WebView popup. Kept apart from Core on purpose:
 * touching Core runs its static System.loadLibrary("core"), which must stay on
 * the background path CoreBoot uses, never on the UI thread of an Activity.
 */
final class PopupHost {
    private static volatile Activity current;

    private PopupHost() {}

    static void set(Activity a) { current = a; }

    static Activity get() { return current; }

    /** Clears the registration only if a is still the registered Activity. */
    static void clear(Activity a) {
        if (current == a) current = null;
    }

    static void runOnMain(Runnable r) {
        Activity a = current;
        if (a != null) a.runOnUiThread(r);
        else new Handler(Looper.getMainLooper()).post(r);
    }
}
