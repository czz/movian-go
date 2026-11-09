package com.moviango.mediaplayer;

import java.io.File;

import android.os.Handler;
import android.os.Message;
import android.os.Environment;
import android.content.Context;
import android.util.Log;
import android.graphics.Bitmap;
import android.app.Activity;
import android.view.SurfaceView;
import android.view.SurfaceHolder;
import android.graphics.PixelFormat;
import android.graphics.Canvas;
import android.text.format.DateFormat;
import android.widget.FrameLayout;
import android.content.pm.PackageManager;

import android.provider.Settings.Secure;


public class Core {


    private static SurfaceView sv;
    private static CoreService mService;

    static {
        // FFmpeg is statically linked inside libcore.so — do NOT
        // loadLibrary("avutil") etc. like upstream did: on firmwares
        // that preload libav* into the zygote as vendor public libs
        // (TIMvision/Movenda STBs, some Sony TVs) the soname resolves
        // to the namespace-inaccessible /system/vendor copy and the
        // app dies with UnsatisfiedLinkError before reaching "core".
        // The bundled android/vendorlibs/<abi>/*.so only satisfy
        // firmware-patched libnativeloader.so DT_NEEDED entries.
        System.loadLibrary("core");
    }

    public static native void coreInit(String settingsdir, String cachedir,
                                       String sdcard, String aid,
                                       int clock_24hrs,
                                       String music,
                                       String pictures,
                                       String movies,
                                       int audio_sample_rate,
                                       int audio_frames_per_buffer);

    public static native void openUri(String uri);

    // These two GLW methods should be called on UI thread

    public static native int glwCreate(VideoRendererProvider vrp);
    public static native void glwDestroy(int id);

    // These four GLW methods should be called on OpenGL renderer thread

    public static native void glwInit(int id);
    public static native void glwFini(int id);
    public static native void glwResize(int id, int width, int height);
    public static native void glwStep(int id);
    public static native void glwFlush(int id);

    // The thread for those are not so important I think...

    public static native void glwMotion(int id, int source, int event, int x, int y, long timestamp);
    public static native boolean glwKeyDown(int id, int code, int unicode,
                                            boolean shift);
    public static native boolean glwKeyUp(int id, int code);

    public static native void permissionResult(boolean ok);

    // System-IME OSK callbacks: seq matches the openOsk request.
    // oskText = live echo of the current input; oskResult = final
    // outcome (text == null means cancelled).
    public static native void oskText(int seq, String text);
    public static native void oskResult(int seq, String text);

    // Create / Destroy subscriptions

    public static native int subValue(int prop, String path, ValueSubscription.Callback cb);
    public static native int subNodes(int prop, String path, NodeSubscriptionCallback cb);
    public static native int unSub(int id);

    // Properties

    public static native int propRetain(int id);

    public static native void propRelease(int id);

    // Dispatch a round of property updates, should only be called on UI thread
    public static native void pollCourier();

    public static native void networkStatusChanged();

    /**
     * Blocking, bounded popup entry for the Go side (called off the UI thread).
     * Returns {"result":"cookie|trapped|userclose|neterror|timeout",
     * "url":..., "topOrigin":...}. waitTimeoutMs is the plugin's cookie wait
     * limit; 0 means only the 30 min session limit applies.
     */
    public static String webPopupPrototype(String uri, String title, String trap,
                                           String waitUrl, String waitName, String owner,
                                           long waitTimeoutMs) {
        Activity a = PopupHost.get();
        if (a == null || uri == null || owner == null || owner.isEmpty())
            return PopupRequest.json("neterror", null, null);
        return WebPopupActivity.open(uri, title, trap, waitUrl, waitName, owner, waitTimeoutMs, a);
    }

    /** JSON array of cookie info strings for uri, or null; returns within 10 s. */
    public static String webPopupPrototypeCookies(String uri, String owner) {
        return WebPopupActivity.cookies(uri, owner);
    }

    /**
     * Removes the clearance-profile cookies a request to host/path would carry,
     * limited to the names in the JSON array namesJson ("" or "[]" means all).
     * Returns {"deleted":[names],"skipped":{...}}, or null on failure; returns
     * within 8 s.
     */
    public static String webPopupPrototypeDeleteCookies(String owner, String host, String path, String namesJson) {
        return WebPopupActivity.deleteCookies(owner, host, path, namesJson);
    }

    public static String webPopupPrototypeUA() {
        return WebPopupActivity.userAgent();
    }

    public static void init(CoreService svc) {

        mService = svc;
        Apps.init(svc);
        Tv.init(svc);

        int clock_24hrs = DateFormat.is24HourFormat(svc) ? 1 : 0;

        coreInit(svc.getFilesDir().getPath(),
                 svc.getCacheDir().getPath(),
                 Environment.getExternalStorageDirectory().toString(),
                 Secure.getString(svc.getContentResolver(), Secure.ANDROID_ID),
                 clock_24hrs,
                 Environment.getExternalStoragePublicDirectory(Environment.DIRECTORY_MUSIC).toString(),
                 Environment.getExternalStoragePublicDirectory(Environment.DIRECTORY_PICTURES).toString(),
                 Environment.getExternalStoragePublicDirectory(Environment.DIRECTORY_MOVIES).toString(),
                 svc.getSystemAudioSampleRate(),
                 svc.getSystemAudioFramesPerBuffer());
    }

    public static Bitmap createBitmap(int width, int height) {
        return Bitmap.createBitmap(width, height, Bitmap.Config.ARGB_8888);
    }

    public static boolean checkPermission(String permission) {
        return mService.checkSelfPermission(permission) ==
            PackageManager.PERMISSION_GRANTED;
    }

    public static void pushBitmap(final Bitmap b) {

        PopupHost.get().runOnUiThread(new Runnable() {
                public void run() {

                    SurfaceHolder sh = sv.getHolder();

                    sh.setFormat(PixelFormat.RGBA_8888);

                    Canvas c = sh.lockCanvas();
                    c.drawBitmap(b, null, sh.getSurfaceFrame(), null);
                    sh.unlockCanvasAndPost(c);
                }
            });
    }
}
