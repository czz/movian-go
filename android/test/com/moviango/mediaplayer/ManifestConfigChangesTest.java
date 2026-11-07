package com.moviango.mediaplayer;

import java.nio.charset.StandardCharsets;
import java.nio.file.Files;
import java.nio.file.Paths;
import java.util.Arrays;
import java.util.HashSet;
import java.util.Set;
import java.util.regex.Matcher;
import java.util.regex.Pattern;

/**
 * Standalone check, no Android runtime or JUnit: the manifest must declare
 * every configuration change WebPopupActivity handles itself. Without one
 * of them Android destroys and recreates the Activity, and onDestroy would
 * end the in-flight challenge as a user close.
 *   javac -d OUT android/test/com/moviango/mediaplayer/ManifestConfigChangesTest.java
 *   java -cp OUT com.moviango.mediaplayer.ManifestConfigChangesTest android/AndroidManifest.xml.in
 */
public final class ManifestConfigChangesTest {
    private static final String[] REQUIRED = {
        "locale", "fontScale", "uiMode", "density", "layoutDirection", "keyboard",
        "keyboardHidden", "navigation", "screenLayout", "smallestScreenSize",
        "orientation", "screenSize",
    };

    public static void main(String[] args) throws Exception {
        String xml = new String(Files.readAllBytes(Paths.get(args[0])), StandardCharsets.UTF_8);
        Matcher m = Pattern.compile("<activity\\s[^>]*android:name=\"\\.WebPopupActivity\"[^>]*>",
                Pattern.DOTALL).matcher(xml);
        if (!m.find()) throw new AssertionError("WebPopupActivity is not declared");
        Matcher c = Pattern.compile("android:configChanges=\"([^\"]*)\"").matcher(m.group());
        if (!c.find()) throw new AssertionError("WebPopupActivity declares no configChanges");
        Set<String> have = new HashSet<String>(Arrays.asList(c.group(1).trim().split("\\s*\\|\\s*")));
        StringBuilder missing = new StringBuilder();
        for (String r : REQUIRED) if (!have.contains(r)) missing.append(' ').append(r);
        if (missing.length() > 0)
            throw new AssertionError("WebPopupActivity does not handle:" + missing);
        System.out.println("ManifestConfigChangesTest: all checks passed");
    }
}
