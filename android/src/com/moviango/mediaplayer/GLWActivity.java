package com.moviango.mediaplayer;

import java.util.concurrent.FutureTask;
import java.util.concurrent.RunnableFuture;
import java.util.concurrent.Callable;

import android.os.Handler;

import android.net.Uri;

import android.os.Bundle;
import android.os.Message;
import android.content.Intent;
import android.content.ServiceConnection;
import android.content.Context;
import android.content.ComponentName;
import android.content.pm.PackageManager;
import android.content.ServiceConnection;
import android.content.Context;
import android.content.ComponentName;
import android.app.Activity;
import android.text.Editable;
import android.text.TextWatcher;
import android.text.InputType;
import android.view.inputmethod.EditorInfo;
import android.view.inputmethod.InputMethodManager;
import android.widget.EditText;
import android.widget.TextView;
import android.view.Menu;
import android.view.KeyEvent;
import android.view.SurfaceView;
import android.view.Window;
import android.view.WindowManager;
import android.util.Log;

import android.os.Environment;
import android.content.ContentUris;
import android.content.Context;
import android.provider.DocumentsContract;
import android.provider.MediaStore;
import android.provider.MediaStore.MediaColumns;
import android.database.Cursor;

import android.widget.FrameLayout;

import android.media.tv.TvContract;
import android.media.tv.TvView;
import android.media.tv.TvContentRating;
import android.media.tv.TvTrackInfo;
import java.util.List;

import android.util.Log;

public class GLWActivity extends Activity implements VideoRendererProvider {

    private final OneShot<Uri> pendingUri = new OneShot<>();
    GLWView mGLWView;
    FrameLayout mRoot;
    SurfaceView sv;
    TvView mTvView;
    private OskEditText mOskEdit;

    private boolean mStarted;

    // Runs on the main thread when the core becomes ready.
    private final Runnable mReadyRunnable = new Runnable() {
            public void run() { startGLW(); }
        };

    private void startGLW() {
        // Only for a started, live activity, once the core is ready,
        // and only once per start.
        if(!mStarted || isFinishing() || mGLWView != null ||
           !CoreBoot.isReady())
            return;
        // remove title
        mRoot = new FrameLayout(this);

        mGLWView = new GLWView(getApplication(), this);
        mRoot.addView(mGLWView);

        setContentView(mRoot);
    }

    @Override
    protected void onStart() {
        super.onStart();
        mStarted = true;
        // Immediately if the core is ready, else when it becomes ready.
        CoreBoot.runWhenReady(mReadyRunnable);
    }

    @Override
    protected void onStop() {
        super.onStop();
        mStarted = false;
        CoreBoot.removeReadyListener(mReadyRunnable);
        // GLW is never created if the core was not ready yet.
        if(mGLWView != null) {
            mGLWView.destroy();
            mGLWView = null;
        }
    }

    @Override
    protected void onCreate(Bundle savedInstanceState) {
        Log.d("Movian", "onCreate");
        super.onCreate(savedInstanceState);
        PopupHost.set(this);
        pendingUri.offer(uriOf(getIntent()));

        requestWindowFeature(Window.FEATURE_NO_TITLE);
        getWindow().addFlags(WindowManager.LayoutParams.FLAG_FULLSCREEN);

        Tv.attach(this);
        startService(new Intent(this, CoreService.class));
    }

    public boolean onKeyUp(int keyCode, KeyEvent event) {

        if(mGLWView != null && mGLWView.keyUp(keyCode, event))
            return true;
        return super.onKeyUp(keyCode, event);
    }


    public boolean onKeyDown(int keyCode, KeyEvent event) {

        if(mGLWView != null && mGLWView.keyDown(keyCode, event))
            return true;
        return super.onKeyDown(keyCode, event);
    }

    @Override
    protected void onNewIntent(Intent intent) {
        super.onNewIntent(intent);
        // singleTask: intents delivered to the running instance end up here.
        // setIntent() lets the onResume handler below pick up the URI
        // (media open while running, or HOME presses when we are the
        // default launcher).
        setIntent(intent);
        pendingUri.offer(uriOf(intent));
    }

    private static Uri uriOf(Intent intent) {
        Uri uri = intent.getData();
        if(uri == null)
            uri = intent.getParcelableExtra("uri");
        return uri;
    }

    @Override
    protected void onResume() {
        Log.d("Movian", "onResume");
        super.onResume();

        // Each delivered intent opens its URI once. Returning from the WebView
        // popup resumes us again and must not reopen the same route.
        Uri uri = pendingUri.take();
        if(uri != null) {
            String u = getRealPathFromUri(uri);
            // Queued (deduplicated) until the core is ready.
            CoreBoot.openUri(u != null ? u : uri.toString());
        }
    }

    @Override
    protected void onPause() {
        Log.d("Movian", "onPause");
        super.onPause();
    }

    @Override
    protected void onDestroy() {
        Log.d("Movian", "onDestroy");
        PopupHost.clear(this);
        CoreBoot.removeReadyListener(mReadyRunnable);
        closeOskView();
        tvUntune();
        Tv.detach(this);
        super.onDestroy();
    }

    // ---- TIF playback (Tv.* statics dispatch onto the UI thread) ----
    // The TvView renders on the normal media surface layer — under the
    // translucent media-overlay GLSurfaceView, like VideoRenderer — so
    // GLW pages/OSD draw on top of the picture.

    public void tvTune(String inputId, long channelId) {
        tvTuneUri(inputId, TvContract.buildChannelUri(channelId));
    }

    // tvTuneUri — tune by raw channel URI. Vendor inputs may accept
    // non-TvContract URIs (e.g. dvb://<onid>.<tsid>.<sid> triplets).
    public void tvTuneUri(String inputId, Uri channelUri) {
        if (mRoot == null)
            return;
        if (mTvView == null) {
            mTvView = new TvView(this) {
                @Override
                public boolean dispatchKeyEvent(KeyEvent event) {
                    // CH+/CH- would be consumed by the vendor session
                    // (it zaps on its own lineup, desyncing our OSD) —
                    // route them to GLW so our zap order applies.
                    int kc = event.getKeyCode();
                    if(kc == KeyEvent.KEYCODE_CHANNEL_UP
                            || kc == KeyEvent.KEYCODE_CHANNEL_DOWN) {
                        if(event.getAction() == KeyEvent.ACTION_DOWN) {
                            if(mGLWView != null)
                                mGLWView.keyDown(kc, event);
                        } else if(event.getAction() == KeyEvent.ACTION_UP) {
                            if(mGLWView != null)
                                mGLWView.keyUp(kc, event);
                        }
                        return true;
                    }
                    return super.dispatchKeyEvent(event);
                }
            };
            // The TvView subtree must never take focus — DPAD keys have
            // to bubble up to GLWActivity.onKeyDown() so GLW sees them.
            mTvView.setFocusable(false);
            mTvView.setDescendantFocusability(
                android.view.ViewGroup.FOCUS_BLOCK_DESCENDANTS);
            mTvView.setTimeShiftPositionCallback(
                new TvView.TimeShiftPositionCallback() {
                    @Override
                    public void onTimeShiftStartPositionChanged(
                            String inputId, long timeMs) {
                        mTsStartMs = timeMs;
                    }
                    @Override
                    public void onTimeShiftCurrentPositionChanged(
                            String inputId, long timeMs) {
                        mTsCurMs = timeMs;
                    }
                });
            mTvView.setCallback(new TvView.TvInputCallback() {
                @Override
                public void onConnectionFailed(String inputId) {
                    Log.e("Movian", "TvView connection failed: " + inputId);
                }
                @Override
                public void onVideoAvailable(String inputId) {
                    Log.d("Movian", "TvView video available: " + inputId);
                }
                @Override
                public void onVideoUnavailable(String inputId, int reason) {
                    Log.d("Movian", "TvView video unavailable: " + inputId
                        + " reason=" + reason);
                }
                @Override
                public void onContentBlocked(String inputId,
                                             TvContentRating rating) {
                    Log.d("Movian", "TvView content blocked: " + inputId);
                }
                @Override
                public void onTrackSelected(String inputId, int type,
                                            String trackId) {}
                @Override
                public void onTracksChanged(String inputId,
                                            List<TvTrackInfo> tracks) {}
            });
            FrameLayout.LayoutParams lp = new FrameLayout.LayoutParams(
                FrameLayout.LayoutParams.MATCH_PARENT,
                FrameLayout.LayoutParams.MATCH_PARENT);
            mRoot.addView(mTvView, 0, lp);
            applyTvRect();
        }
        mTsStartMs = mTsCurMs = -1;
        mTvView.tune(inputId, channelUri);
    }

    // mTvRect — last rect requested by the backend (pixels, empty =
    // fullscreen). Stored so it also applies to a TvView created
    // later by tvTuneUri.
    private final android.graphics.Rect mTvRect =
        new android.graphics.Rect(0, 0, -1, -1);

    public void tvSetVideoRect(int l, int t, int r, int b) {
        mTvRect.set(l, t, r, b);
        applyTvRect();
    }

    private void applyTvRect() {
        if (mTvView == null)
            return;
        FrameLayout.LayoutParams lp;
        if (mTvRect.width() <= 0 || mTvRect.height() <= 0) {
            lp = new FrameLayout.LayoutParams(
                FrameLayout.LayoutParams.MATCH_PARENT,
                FrameLayout.LayoutParams.MATCH_PARENT);
        } else {
            lp = new FrameLayout.LayoutParams(
                mTvRect.width(), mTvRect.height());
            lp.leftMargin = mTvRect.left;
            lp.topMargin = mTvRect.top;
        }
        mTvView.setLayoutParams(lp);
    }

    public boolean tvIsTuned() {
        return mTvView != null;
    }

    public void tvUntune() {
        if (mTvView == null)
            return;
        mTvView.reset();
        mRoot.removeView(mTvView);
        mTvView = null;
    }

    // Timeshift controls — vendor sessions expose a rolling buffer;
    // positions are wall-clock milliseconds.
    public void tvPause(boolean paused) {
        if (mTvView == null)
            return;
        if (paused)
            mTvView.timeShiftPause();
        else
            mTvView.timeShiftResume();
    }

    public void tvSeekTo(long ms) {
        if (mTvView != null)
            mTvView.timeShiftSeekTo(ms);
    }

    // "start;cur" — buffer start and current playback position. The
    // TvView polls the session when a TimeShiftPositionCallback is
    // registered and pushes updates here.
    private long mTsStartMs = -1, mTsCurMs = -1;

    public String tvTimeshift() {
        if (mTvView == null || mTsStartMs < 0 || mTsCurMs < 0)
            return "";
        return mTsStartMs + ";" + mTsCurMs;
    }

    // Track list as JSON: [{type,id,lang,selected}]. Types follow
    // TvTrackInfo (0=video, 1=audio, 2=subtitle).
    public String tvTracks() {
        if (mTvView == null)
            return "[]";
        org.json.JSONArray arr = new org.json.JSONArray();
        int[] types = { TvTrackInfo.TYPE_VIDEO, TvTrackInfo.TYPE_AUDIO,
                        TvTrackInfo.TYPE_SUBTITLE };
        for (int type : types) {
            java.util.List<TvTrackInfo> tracks = mTvView.getTracks(type);
            if (tracks == null)
                continue;
            String sel = mTvView.getSelectedTrack(type);
            for (TvTrackInfo t : tracks) {
                org.json.JSONObject o = new org.json.JSONObject();
                try {
                    o.put("type", type);
                    o.put("id", t.getId());
                    CharSequence d = t.getDescription();
                    String lang = t.getLanguage();
                    o.put("lang", lang != null ? lang : "");
                    o.put("desc", d != null ? d.toString() : "");
                    o.put("selected", t.getId().equals(sel));
                    if (type == TvTrackInfo.TYPE_VIDEO) {
                        o.put("w", t.getVideoWidth());
                        o.put("h", t.getVideoHeight());
                        o.put("fps",
                              (double) t.getVideoFrameRate());
                    }
                    if (type == TvTrackInfo.TYPE_AUDIO) {
                        o.put("ch", t.getAudioChannelCount());
                        o.put("sr", t.getAudioSampleRate());
                    }
                } catch (org.json.JSONException e) {}
                arr.put(o);
            }
        }
        return arr.toString();
    }

    public void tvSelectTrack(int type, String id) {
        if (mTvView != null)
            mTvView.selectTrack(type, id == null || id.isEmpty() ? null : id);
    }

    // These does not execute on the main ui thread so we need to dispatch

    @Override
    public VideoRenderer createVideoRenderer() throws Exception {

        RunnableFuture<VideoRenderer> f = new FutureTask<VideoRenderer>(new Callable<VideoRenderer>() {
                public VideoRenderer call() {
                    VideoRenderer vr = new VideoRenderer(GLWActivity.this);
                    mRoot.addView(vr);
                    return vr;
                }
            });

        runOnUiThread(f);
        return f.get();
    }

    @Override
    public void destroyVideoRenderer(final VideoRenderer vr) {

        runOnUiThread(new Runnable() {
                public void run() {
                    mRoot.removeView(vr);
                }
            });
    }


    @Override
    public void disableScreenSaver() {
        runOnUiThread(new Runnable() {
                public void run() {
                    getWindow().addFlags(WindowManager.LayoutParams.FLAG_KEEP_SCREEN_ON);
                }
            });
    }

    @Override
    public void enableScreenSaver() {
        runOnUiThread(new Runnable() {
                public void run() {
                    getWindow().clearFlags(WindowManager.LayoutParams.FLAG_KEEP_SCREEN_ON);
                }
            });
    }

    @Override
    public void sysHome() {
        runOnUiThread(new Runnable() {
                public void run() {
                    // Launcher mode: Movian IS the home app — firing a
                    // HOME intent would just switch to the OEM launcher
                    // (which always wins resolution on locked firmware).
                    // A real launcher stays put on back-at-root.
                    if (HomeKeyService.aliasEnabled(GLWActivity.this))
                        return;
                    Intent startMain = new Intent(Intent.ACTION_MAIN);
                    startMain.addCategory(Intent.CATEGORY_HOME);
                    startMain.setFlags(Intent.FLAG_ACTIVITY_NEW_TASK);
                    startActivity(startMain);
                }
            });
    }

    // openOsk — called from the GLW thread via JNI (ml_jni_vrp_open_osk)
    // when the "Use system keyboard" setting is on. No dialog window:
    // a 1px transparent EditText takes focus, the system IME opens over
    // the Movian UI, every keystroke is echoed into the Movian text
    // field via Core.oskText and the IME action key submits through
    // Core.oskResult(seq, text|null).
    public void openOsk(final String title, final String text,
                        final int seq, final int password) {
        runOnUiThread(new Runnable() {
                public void run() {
                    if (isFinishing() || isDestroyed()) {
                        Core.oskResult(seq, null);
                        return;
                    }
                    closeOskView();

                    final int fseq = seq;
                    final OskEditText et = new OskEditText(GLWActivity.this, seq);
                    et.setSingleLine(true);
                    et.setAlpha(0f);
                    et.setInputType(password != 0
                        ? InputType.TYPE_CLASS_TEXT | InputType.TYPE_TEXT_VARIATION_PASSWORD
                        : InputType.TYPE_CLASS_TEXT);
                    et.setImeOptions(EditorInfo.IME_ACTION_DONE);
                    if (text != null)
                        et.setText(text);

                    et.addTextChangedListener(new TextWatcher() {
                            public void beforeTextChanged(CharSequence s, int st, int c, int a) {}
                            public void onTextChanged(CharSequence s, int st, int b, int c) {}
                            public void afterTextChanged(Editable s) {
                                Core.oskText(fseq, s.toString());
                            }
                        });

                    et.setOnEditorActionListener(new TextView.OnEditorActionListener() {
                            public boolean onEditorAction(TextView v, int actionId, KeyEvent ev) {
                                if (actionId == EditorInfo.IME_ACTION_DONE
                                    || (ev != null && ev.getKeyCode() == KeyEvent.KEYCODE_ENTER
                                        && ev.getAction() == KeyEvent.ACTION_UP)) {
                                    finishOsk(fseq, et.getText().toString());
                                    return true;
                                }
                                return false;
                            }
                        });

                    et.setLayoutParams(new FrameLayout.LayoutParams(1, 1));
                    mRoot.addView(et);
                    mOskEdit = et;
                    et.requestFocus();
                    InputMethodManager imm = (InputMethodManager)
                        getSystemService(Context.INPUT_METHOD_SERVICE);
                    if (imm != null)
                        imm.showSoftInput(et, InputMethodManager.SHOW_IMPLICIT);
                }
            });
    }

    // finishOsk — detach the hidden editor, hide the IME and report
    // the outcome to the native side.
    private void finishOsk(int seq, String text) {
        closeOskView();
        Core.oskResult(seq, text);
    }

    private void closeOskView() {
        if (mOskEdit == null)
            return;
        InputMethodManager imm = (InputMethodManager)
            getSystemService(Context.INPUT_METHOD_SERVICE);
        if (imm != null)
            imm.hideSoftInputFromWindow(mOskEdit.getWindowToken(), 0);
        mRoot.removeView(mOskEdit);
        mOskEdit = null;
    }

    // OskEditText — invisible input sink for the system IME. BACK is
    // intercepted before the IME sees it so it cancels the Movian
    // input instead of just hiding the keyboard.
    private class OskEditText extends EditText {
        private final int mSeq;

        OskEditText(Context ctx, int seq) {
            super(ctx);
            mSeq = seq;
        }

        @Override
        public boolean onKeyPreIme(int keyCode, KeyEvent event) {
            if (keyCode == KeyEvent.KEYCODE_BACK) {
                if (event.getAction() == KeyEvent.ACTION_DOWN)
                    finishOsk(mSeq, null);
                return true;
            }
            return super.onKeyPreIme(keyCode, event);
        }
    }

    @Override
    public void askPermission(final String permission) {
        runOnUiThread(new Runnable() {
                public void run() {
                    // If the activity can no longer host the runtime
                    // dialog, fail fast — the native caller blocks in
                    // permissionCond.Wait() and must never be left
                    // waiting for a result that cannot arrive.
                    if (isFinishing() || isDestroyed()) {
                        Core.permissionResult(false);
                        return;
                    }
                    try {
                        requestPermissions(new String[] {permission}, 1);
                    } catch (Exception e) {
                        Core.permissionResult(false);
                    }
                }
            });
    }

    @Override
    public void onRequestPermissionsResult(int requestCode,
                                           String permissions[],
                                           int[] grantResults) {
        if (requestCode != 1 || !CoreBoot.isReady())
            return;
        // grantResults is empty when the request is cancelled — indexing
        // it would throw and leave the native wait blocked forever.
        Core.permissionResult(grantResults.length > 0 &&
                              grantResults[0] == PackageManager.PERMISSION_GRANTED);
    }

    public String getRealPathFromUri(final Uri uri) {
        // DocumentProvider
        if (DocumentsContract.isDocumentUri(this, uri)) {
            // ExternalStorageProvider
            if (isExternalStorageDocument(uri)) {
                final String docId = DocumentsContract.getDocumentId(uri);
                final String[] split = docId.split(":");
                final String type = split[0];

                if ("primary".equalsIgnoreCase(type)) {
                    return "es:///" + split[1];
                }
            }
            // DownloadsProvider
            else if (isDownloadsDocument(uri)) {

                final String id = DocumentsContract.getDocumentId(uri);
                final Uri contentUri = ContentUris.withAppendedId(
                        Uri.parse("content://downloads/public_downloads"), Long.valueOf(id));

                return getDataColumn(this, contentUri, null, null);
            }
            // MediaProvider
            else if (isMediaDocument(uri)) {
                final String docId = DocumentsContract.getDocumentId(uri);
                final String[] split = docId.split(":");
                final String type = split[0];

                Uri contentUri = null;
                if ("image".equals(type)) {
                    contentUri = MediaStore.Images.Media.EXTERNAL_CONTENT_URI;
                } else if ("video".equals(type)) {
                    contentUri = MediaStore.Video.Media.EXTERNAL_CONTENT_URI;
                } else if ("audio".equals(type)) {
                    contentUri = MediaStore.Audio.Media.EXTERNAL_CONTENT_URI;
                }

                final String selection = "_id=?";
                final String[] selectionArgs = new String[]{
                        split[1]
                };

                return getDataColumn(this, contentUri, selection, selectionArgs);
            }
        }
        // MediaStore (and general)
        else if ("content".equalsIgnoreCase(uri.getScheme())) {

            // Return the remote address
            if (isGooglePhotosUri(uri))
                return uri.getLastPathSegment();

            return getDataColumn(this, uri, null, null);
        }
        // File
        else if ("file".equalsIgnoreCase(uri.getScheme())) {
            return uri.getPath();
        }

        return null;
    }

    private String getDataColumn(Context context, Uri uri, String selection,
                                 String[] selectionArgs) {

        Cursor cursor = null;
        final String column = "_data";
        final String[] projection = {
                column
        };

        try {
            cursor = context.getContentResolver().query(uri, projection, selection, selectionArgs,
                    null);
            if (cursor != null && cursor.moveToFirst()) {
                final int index = cursor.getColumnIndexOrThrow(column);
                return cursor.getString(index);
            }
        } finally {
            if (cursor != null)
                cursor.close();
        }
        return null;
    }

    private boolean isExternalStorageDocument(Uri uri) {
        return "com.android.externalstorage.documents".equals(uri.getAuthority());
    }

    private boolean isDownloadsDocument(Uri uri) {
        return "com.android.providers.downloads.documents".equals(uri.getAuthority());
    }

    private boolean isMediaDocument(Uri uri) {
        return "com.android.providers.media.documents".equals(uri.getAuthority());
    }

    private boolean isGooglePhotosUri(Uri uri) {
        return "com.google.android.apps.photos.content".equals(uri.getAuthority());
    }
}

