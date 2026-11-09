package com.moviango.mediaplayer;

import java.text.ParseException;
import java.text.SimpleDateFormat;
import java.util.ArrayList;
import java.util.Date;
import java.util.List;
import java.util.Locale;
import java.util.Set;
import java.util.TimeZone;

/**
 * Plans the removal of WebView cookies. Plain Java (no Android types) so the
 * matching is testable with javac alone.
 *
 * CookieManager has no delete-by-name call. A cookie is removed by setting an
 * already-expired one with the same name, Domain, Path and (for a Partitioned
 * cookie) Partitioned attribute, on a URL the original cookie matches. The
 * input is CookieManagerCompat.getCookieInfo(): Set-Cookie strings of the
 * cookies a request to the URL would carry, so the Partitioned ones listed are
 * those of the first-party partition only. This class re-checks host, path,
 * expiry and name so the rules are stated and tested in one place.
 */
final class CookieDelete {
    /** One expiring Set-Cookie to hand to CookieManager.setCookie(url, ...). */
    static final class Expiry {
        final String name, url, setCookie;
        final boolean partitioned;

        Expiry(String name, String url, String setCookie, boolean partitioned) {
            this.name = name;
            this.url = url;
            this.setCookie = setCookie;
            this.partitioned = partitioned;
        }
    }

    static final class Plan {
        final List<Expiry> expire = new ArrayList<Expiry>();
        int expired, otherHost, otherPath, filtered, partitioned;

        List<String> names() {
            List<String> out = new ArrayList<String>();
            for (Expiry e : expire) out.add(e.name);
            return out;
        }
    }

    private CookieDelete() {}

    /**
     * @param lines   Set-Cookie strings from getCookieInfo for the request URL
     * @param host    request host
     * @param path    request path ("/" when empty)
     * @param names   cookie names to remove (case-sensitive); null or empty means all
     * @param nowMillis current time
     */
    static Plan plan(List<String> lines, String host, String path, Set<String> names, long nowMillis) {
        Plan p = new Plan();
        if (lines == null || host == null || host.isEmpty() || !safe(host)) return p;
        host = host.toLowerCase(Locale.ROOT);
        if (path == null || path.isEmpty()) path = "/";
        for (String line : lines) {
            if (line == null || !safe(line)) continue;
            String[] parts = line.split(";");
            int eq = parts[0].indexOf('=');
            if (eq <= 0) continue;
            String name = parts[0].substring(0, eq).trim();
            if (name.isEmpty() || !tokenSafe(name)) continue;
            String domain = null, cpath = null;
            boolean secure = false, httpOnly = false, partitioned = false, expiredCookie = false;
            boolean badAttr = false;
            Long maxAge = null;
            for (int i = 1; i < parts.length; i++) {
                String attr = parts[i].trim();
                int e = attr.indexOf('=');
                String key = (e < 0 ? attr : attr.substring(0, e)).trim().toLowerCase(Locale.ROOT);
                String val = e < 0 ? "" : attr.substring(e + 1).trim();
                switch (key) {
                    case "domain": if (val.isEmpty()) badAttr = true; else domain = val.toLowerCase(Locale.ROOT); break;
                    case "path": if (val.isEmpty()) badAttr = true; else cpath = val; break;
                    case "secure": secure = true; break;
                    case "httponly": httpOnly = true; break;
                    case "partitioned": partitioned = true; break;
                    case "max-age":
                        try { maxAge = Long.valueOf(val); } catch (NumberFormatException ex) { badAttr = true; }
                        break;
                    case "expires": {
                        Long t = parseExpires(val);
                        if (t != null && t.longValue() <= nowMillis) expiredCookie = true;
                        break;
                    }
                    default: break;
                }
            }
            if (badAttr || cpath == null || !cpath.startsWith("/")) continue;
            if (maxAge != null) expiredCookie = maxAge.longValue() <= 0; // Max-Age wins over Expires
            if (expiredCookie) { p.expired++; continue; }
            // getCookieInfo serializes the stored domain: a leading dot marks a
            // domain cookie, no dot a host-only one (which has no Domain
            // attribute when set and is sent to that exact host only).
            boolean hostOnly = domain != null && !domain.startsWith(".");
            if (domain != null && (hostOnly ? !host.equals(domain) : !domainMatches(host, domain))) {
                p.otherHost++;
                continue;
            }
            if (!pathMatches(path, cpath)) { p.otherPath++; continue; }
            if (names != null && !names.isEmpty() && !names.contains(name)) { p.filtered++; continue; }
            StringBuilder sc = new StringBuilder(name).append("=; Max-Age=0; Expires=Thu, 01 Jan 1970 00:00:00 GMT");
            if (domain != null && !hostOnly) sc.append("; Domain=").append(domainAttr(line, domain));
            sc.append("; Path=").append(cpath);
            if (secure) sc.append("; Secure");
            if (httpOnly) sc.append("; HttpOnly"); // required by the __Http- and __Host-Http- prefixes
            if (partitioned) { sc.append("; Partitioned"); p.partitioned++; }
            p.expire.add(new Expiry(name, "https://" + host + "/", sc.toString(), partitioned));
        }
        return p;
    }

    /** The Domain attribute exactly as the cookie spelled it (a leading dot matters to some stores). */
    private static String domainAttr(String line, String lowered) {
        for (String part : line.split(";")) {
            String a = part.trim();
            int e = a.indexOf('=');
            if (e > 0 && a.substring(0, e).trim().equalsIgnoreCase("domain")
                    && a.substring(e + 1).trim().toLowerCase(Locale.ROOT).equals(lowered)) {
                return a.substring(e + 1).trim();
            }
        }
        return lowered;
    }

    static boolean domainMatches(String host, String domain) {
        if (domain.startsWith(".")) domain = domain.substring(1);
        if (domain.isEmpty()) return false;
        return host.equals(domain) || host.endsWith("." + domain);
    }

    /** RFC 6265 5.1.4 path-match of the request path against the cookie path. */
    static boolean pathMatches(String reqPath, String cookiePath) {
        if (reqPath.equals(cookiePath)) return true;
        if (!reqPath.startsWith(cookiePath)) return false;
        return cookiePath.endsWith("/") || reqPath.charAt(cookiePath.length()) == '/';
    }

    private static Long parseExpires(String v) {
        String[] formats = {"EEE, dd MMM yyyy HH:mm:ss zzz", "EEE, dd-MMM-yyyy HH:mm:ss zzz",
                "EEEE, dd-MMM-yy HH:mm:ss zzz", "EEE MMM d HH:mm:ss yyyy"};
        for (String f : formats) {
            SimpleDateFormat sdf = new SimpleDateFormat(f, Locale.US);
            sdf.setTimeZone(TimeZone.getTimeZone("GMT"));
            sdf.setLenient(false);
            try {
                Date d = sdf.parse(v);
                if (d != null) return Long.valueOf(d.getTime());
            } catch (ParseException ignored) { /* try the next format */ }
        }
        return null; // unparsable: not treated as expired
    }

    /** No control characters: the string goes into a Set-Cookie header. */
    private static boolean safe(String s) {
        for (int i = 0; i < s.length(); i++) {
            char c = s.charAt(i);
            if (c < 0x20 || c == 0x7f) return false;
        }
        return true;
    }

    private static boolean tokenSafe(String s) {
        for (int i = 0; i < s.length(); i++) {
            char c = s.charAt(i);
            if (c <= ' ' || c == ';' || c == ',' || c == '=' || c == '"') return false;
        }
        return true;
    }

    /** {"deleted":[names],"skipped":{...}} — the answer to the Go side. */
    static String resultJson(List<String> deleted, Plan p) {
        StringBuilder b = new StringBuilder("{\"deleted\":[");
        for (int i = 0; i < deleted.size(); i++) {
            if (i > 0) b.append(',');
            b.append('"');
            String s = deleted.get(i);
            for (int j = 0; j < s.length(); j++) {
                char c = s.charAt(j);
                if (c == '"' || c == '\\') b.append('\\').append(c);
                else if (c < 0x20) b.append(String.format(Locale.ROOT, "\\u%04x", (int) c));
                else b.append(c);
            }
            b.append('"');
        }
        return b.append("],\"skipped\":{\"expired\":").append(p.expired)
                .append(",\"otherHost\":").append(p.otherHost)
                .append(",\"otherPath\":").append(p.otherPath)
                .append(",\"filtered\":").append(p.filtered)
                .append("}}").toString();
    }
}
