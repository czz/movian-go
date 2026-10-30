package com.moviango.mediaplayer;

import java.io.File;
import android.net.Uri;

import android.app.Service;
import android.content.Intent;
import android.os.Bundle;
import android.os.IBinder;
import android.content.IntentFilter;
import android.content.BroadcastReceiver;
import android.content.Context;
import android.net.ConnectivityManager;
import android.net.LinkAddress;
import android.net.LinkProperties;
import android.net.Network;
import android.os.Build;
import android.util.Log;

import android.media.AudioManager;

public class CoreService extends Service {

    final IntentFilter networkChecker = new IntentFilter();

    // Registered only once the core is ready, so the callback can
    // never touch class Core early.
    private BroadcastReceiver networkCheckerAction = new BroadcastReceiver() {
            @Override
            public void onReceive(final Context context, final Intent intent) {
                if(CoreBoot.isReady())
                    Core.networkStatusChanged();
            }
        };

    // CONNECTIVITY_ACTION is not delivered to the app on recent Android
    // (seen on Android 16), so API 24+ listens for the default network
    // instead. A reconnect brings several callbacks; the engine folds them
    // into one round. A link with no IPv4 address yet (DHCP is still
    // running) is skipped: onLinkPropertiesChanged fires again once it has
    // one. Like the receiver, it is registered only once the core is ready.
    private ConnectivityManager.NetworkCallback defaultNetworkCallback;

    private static boolean hasIPv4(LinkProperties lp) {
        if (lp == null) {
            return false;
        }
        for (LinkAddress la : lp.getLinkAddresses()) {
            if (la.getAddress() instanceof java.net.Inet4Address) {
                return true;
            }
        }
        return false;
    }

    private boolean receiverRegistered;

    private final Runnable registerNetworkReceiver = new Runnable() {
            public void run() {
                if(receiverRegistered)
                    return;
                receiverRegistered = true;
                ConnectivityManager cm =
                    (ConnectivityManager) getSystemService(Context.CONNECTIVITY_SERVICE);
                if (Build.VERSION.SDK_INT >= 24 && cm != null) {
                    defaultNetworkCallback = new ConnectivityManager.NetworkCallback() {
                            @Override
                            public void onAvailable(final Network network) {
                                Core.networkStatusChanged();
                            }

                            @Override
                            public void onLinkPropertiesChanged(final Network network,
                                                                final LinkProperties lp) {
                                if (hasIPv4(lp)) {
                                    Core.networkStatusChanged();
                                }
                            }

                            @Override
                            public void onLost(final Network network) {
                                Core.networkStatusChanged();
                            }
                        };
                    cm.registerDefaultNetworkCallback(defaultNetworkCallback);
                } else {
                    registerReceiver(networkCheckerAction, networkChecker);
                }
            }
        };

    @Override
    public void onCreate() {
        Log.d("Movian", "Coreservice Init");

        networkChecker.addAction(android.net.ConnectivityManager.CONNECTIVITY_ACTION);
        CoreBoot.start(this);
        CoreBoot.runWhenReady(registerNetworkReceiver);
    }

    @Override
    public void onDestroy() {
        CoreBoot.removeReadyListener(registerNetworkReceiver);
        if(receiverRegistered) {
            receiverRegistered = false;
            if (defaultNetworkCallback != null) {
                ConnectivityManager cm =
                    (ConnectivityManager) getSystemService(Context.CONNECTIVITY_SERVICE);
                if (cm != null) {
                    try {
                        cm.unregisterNetworkCallback(defaultNetworkCallback);
                    } catch (IllegalArgumentException e) {
                        // already unregistered
                    }
                }
                defaultNetworkCallback = null;
            } else {
                unregisterReceiver(networkCheckerAction);
            }
        }
        super.onDestroy();
    }

    @Override
    public int onStartCommand(Intent intent, int flags, int startId) {
        return START_NOT_STICKY;
    }

    @Override
    public IBinder onBind(Intent intent) {
        return null;
    }

    public int getSystemAudioSampleRate() {

        if(Build.VERSION.SDK_INT >= 21) {
            AudioManager audioManager = (AudioManager) this.getSystemService(Context.AUDIO_SERVICE);
            String sr = audioManager.getProperty(AudioManager.PROPERTY_OUTPUT_SAMPLE_RATE);
            return Integer.parseInt(sr);
        }
        return 0;
    }

    public int getSystemAudioFramesPerBuffer() {

        if(Build.VERSION.SDK_INT >= 21) {
            AudioManager audioManager = (AudioManager) this.getSystemService(Context.AUDIO_SERVICE);
            String sr = audioManager.getProperty(AudioManager.PROPERTY_OUTPUT_FRAMES_PER_BUFFER);
            return Integer.parseInt(sr);
        }
        return 0;
    }
}

