package com.moviango.mediaplayer;

import java.util.Arrays;
import java.util.Collections;
import java.util.HashSet;
import java.util.List;
import java.util.Set;

/**
 * Standalone checks for CookieDelete; no Android runtime or JUnit:
 *   javac -d OUT android/src/com/moviango/mediaplayer/CookieDelete.java \
 *       android/test/com/moviango/mediaplayer/CookieDeleteTest.java
 *   java -cp OUT com.moviango.mediaplayer.CookieDeleteTest
 */
public final class CookieDeleteTest {
    // 2026-11-06 12:00:00 UTC
    private static final long NOW = 1793966400000L;
    private static final String HOST = "tracker.example";

    public static void main(String[] args) {
        allMatchingCookiesPlanned();
        hostOnlyCookieHasNoDomain();
        httpPrefixKeepsHttpOnly();
        hostOnlyOnAnotherHostSkipped();
        otherHostSkipped();
        parentDomainMatches();
        pathPrefixMatching();
        nameFilter();
        nameFilterEmptyMeansAll();
        expiredByMaxAgeSkipped();
        expiredByExpiresSkipped();
        futureExpiresPlanned();
        unparsableExpiresIsNotExpired();
        partitionedKeepsAttribute();
        secureOnlyWhenOriginalWas();
        unsafeValuesSkipped();
        malformedLineSkipped();
        emptyInputPlansNothing();
        resultJsonShape();
        System.out.println("CookieDeleteTest: all checks passed");
    }

    private static Set<String> set(String... n) { return new HashSet<String>(Arrays.asList(n)); }

    private static CookieDelete.Plan plan(String host, String path, Set<String> names, String... lines) {
        return CookieDelete.plan(Arrays.asList(lines), host, path, names, NOW);
    }

    static void allMatchingCookiesPlanned() {
        CookieDelete.Plan p = plan(HOST, "/forum/index.php", null,
                "cf_clearance=tok; Domain=.tracker.example; Path=/; Secure; HttpOnly; Max-Age=3600",
                "bb_session=1; Path=/forum; Secure");
        require(p.expire.size() == 2, "expected 2 planned, got " + p.expire.size());
        require(p.expire.get(0).name.equals("cf_clearance"), "first name " + p.expire.get(0).name);
        require(p.expire.get(1).name.equals("bb_session"), "second name " + p.expire.get(1).name);
        require(p.expire.get(0).url.equals("https://tracker.example/"), "url " + p.expire.get(0).url);
        String sc = p.expire.get(0).setCookie;
        require(sc.startsWith("cf_clearance=;"), "value not blank: " + sc);
        require(sc.contains("Max-Age=0"), "no Max-Age=0: " + sc);
        require(sc.contains("Path=/;") || sc.contains("Path=/ ") || sc.endsWith("Path=/") || sc.contains("Path=/"), "no path: " + sc);
        require(sc.contains("Domain=.tracker.example"), "no domain: " + sc);
        require(sc.contains("HttpOnly"), "HttpOnly lost: " + sc);
        require(!sc.contains("tok"), "value leaked into the expiry: " + sc);
    }

    static void hostOnlyCookieHasNoDomain() {
        // Both shapes of a host-only cookie: no Domain, and the WebView's
        // serialization of the stored domain without a leading dot.
        CookieDelete.Plan p = plan(HOST, "/", null,
                "sid=1; Path=/; Secure",
                "__Host-id=1; Domain=tracker.example; Path=/; Secure; HttpOnly");
        require(p.expire.size() == 2, "host-only cookies not planned: " + names(p));
        for (CookieDelete.Expiry e : p.expire)
            require(!e.setCookie.contains("Domain="), "host-only got a Domain: " + e.setCookie);
        require(p.expire.get(1).setCookie.contains("Secure") && p.expire.get(1).setCookie.contains("Path=/"),
                "__Host- twin lacks Secure/Path=/: " + p.expire.get(1).setCookie);
    }

    static void httpPrefixKeepsHttpOnly() {
        CookieDelete.Plan p = plan(HOST, "/", null,
                "__Http-a=1; Path=/; Secure; HttpOnly",
                "__Host-Http-b=1; Domain=tracker.example; Path=/; Secure; HttpOnly",
                "plain=1; Path=/; Secure");
        require(p.expire.get(0).setCookie.contains("; HttpOnly"), "__Http- lost HttpOnly");
        require(p.expire.get(1).setCookie.contains("; HttpOnly") && !p.expire.get(1).setCookie.contains("Domain="),
                "__Host-Http- twin: " + p.expire.get(1).setCookie);
        require(!p.expire.get(2).setCookie.contains("HttpOnly"), "HttpOnly invented");
    }

    static void hostOnlyOnAnotherHostSkipped() {
        CookieDelete.Plan p = plan("www.tracker.example", "/", null, "sid=1; Domain=tracker.example; Path=/; Secure");
        require(p.expire.isEmpty() && p.otherHost == 1, "host-only of the parent host planned for www");
    }

    static void otherHostSkipped() {
        CookieDelete.Plan p = plan(HOST, "/", null,
                "a=1; Domain=other.example; Path=/; Secure",
                "b=1; Domain=.ker.example; Path=/; Secure",
                "c=1; Domain=.example; Path=/; Secure");
        // .example is a suffix of the host label-wise, so it domain-matches;
        // other.example and ker.example do not.
        require(p.expire.size() == 1 && p.expire.get(0).name.equals("c"),
                "wrong survivors: " + names(p));
        require(p.otherHost == 2, "otherHost=" + p.otherHost);
    }

    static void parentDomainMatches() {
        CookieDelete.Plan p = plan("www.tracker.example", "/", null,
                "a=1; Domain=.tracker.example; Path=/; Secure",
                "b=1; Domain=tracker.example; Path=/; Secure");
        // b is host-only on the parent host: not sent to www, so not ours.
        require(names(p).equals("[a]"), "parent domain: " + names(p));
        require(p.expire.get(0).setCookie.contains("Domain=.tracker.example"), "dot lost: " + p.expire.get(0).setCookie);
        require(p.expire.get(0).url.equals("https://www.tracker.example/"), "url " + p.expire.get(0).url);
    }

    static void pathPrefixMatching() {
        CookieDelete.Plan p = plan(HOST, "/forum/topic", null,
                "a=1; Path=/forum; Secure",
                "b=1; Path=/forum/; Secure",
                "c=1; Path=/forum/topic; Secure",
                "d=1; Path=/forumx; Secure",
                "e=1; Path=/forum/topic/deep; Secure");
        require(names(p).equals("[a, b, c]"), "path match: " + names(p));
        require(p.otherPath == 2, "otherPath=" + p.otherPath);
    }

    static void nameFilter() {
        CookieDelete.Plan p = plan(HOST, "/", set("cf_clearance"),
                "cf_clearance=1; Path=/; Secure", "other=2; Path=/; Secure", "CF_CLEARANCE=3; Path=/; Secure");
        require(names(p).equals("[cf_clearance]"), "name filter (case-sensitive): " + names(p));
        require(p.filtered == 2, "filtered=" + p.filtered);
    }

    static void nameFilterEmptyMeansAll() {
        CookieDelete.Plan p = plan(HOST, "/", Collections.<String>emptySet(),
                "a=1; Path=/; Secure", "b=2; Path=/; Secure");
        require(p.expire.size() == 2, "empty filter should mean all");
    }

    static void expiredByMaxAgeSkipped() {
        CookieDelete.Plan p = plan(HOST, "/", null,
                "gone=1; Path=/; Secure; Max-Age=0",
                "neg=1; Path=/; Secure; Max-Age=-5",
                "live=1; Path=/; Secure; Max-Age=60");
        require(names(p).equals("[live]"), "max-age: " + names(p));
        require(p.expired == 2, "expired=" + p.expired);
    }

    static void expiredByExpiresSkipped() {
        CookieDelete.Plan p = plan(HOST, "/", null,
                "old=1; Path=/; Secure; Expires=Thu, 01 Jan 2026 00:00:00 GMT");
        require(p.expire.isEmpty(), "expired cookie planned");
        require(p.expired == 1, "expired=" + p.expired);
    }

    static void futureExpiresPlanned() {
        CookieDelete.Plan p = plan(HOST, "/", null,
                "new=1; Path=/; Secure; Expires=Fri, 01 Jan 2027 00:00:00 GMT");
        require(names(p).equals("[new]"), "future cookie: " + names(p));
    }

    static void unparsableExpiresIsNotExpired() {
        CookieDelete.Plan p = plan(HOST, "/", null, "x=1; Path=/; Secure; Expires=garbage");
        require(names(p).equals("[x]"), "garbage expires dropped the cookie");
    }

    static void partitionedKeepsAttribute() {
        CookieDelete.Plan p = plan(HOST, "/", null,
                "cf_clearance=tok; Domain=.tracker.example; Path=/; Secure; Partitioned",
                "plain=1; Path=/; Secure");
        require(p.expire.get(0).partitioned && p.expire.get(0).setCookie.contains("Partitioned"),
                "partitioned lost: " + p.expire.get(0).setCookie);
        require(!p.expire.get(1).partitioned && !p.expire.get(1).setCookie.contains("Partitioned"),
                "plain cookie got Partitioned");
        require(p.partitioned == 1, "partitioned count=" + p.partitioned);
    }

    static void secureOnlyWhenOriginalWas() {
        CookieDelete.Plan p = plan(HOST, "/", null, "a=1; Path=/; Secure", "b=1; Path=/");
        require(p.expire.get(0).setCookie.contains("Secure"), "secure lost");
        require(!p.expire.get(1).setCookie.contains("Secure"), "secure invented");
    }

    static void unsafeValuesSkipped() {
        CookieDelete.Plan p = plan(HOST, "/", null,
                "a=1; Path=/x\r\nSet-Cookie: evil=1; Secure",
                "b c=1; Path=/; Secure",
                "ok=1; Path=/; Secure");
        require(names(p).equals("[ok]"), "unsafe line survived: " + names(p));
    }

    static void malformedLineSkipped() {
        CookieDelete.Plan p = plan(HOST, "/", null, "", "=novalue; Path=/", "nopath; Secure", "n=1; Secure");
        // n=1 has no Path attribute: the default path is unknown, so it is not planned.
        require(p.expire.isEmpty(), "malformed planned: " + names(p));
    }

    static void emptyInputPlansNothing() {
        require(CookieDelete.plan(null, HOST, "/", null, NOW).expire.isEmpty(), "null lines");
        require(CookieDelete.plan(Collections.<String>emptyList(), HOST, "/", null, NOW).expire.isEmpty(), "empty lines");
        require(CookieDelete.plan(Collections.singletonList("a=1; Path=/"), "", "/", null, NOW).expire.isEmpty(), "empty host");
    }

    static void resultJsonShape() {
        CookieDelete.Plan p = plan(HOST, "/", null,
                "a=1; Path=/; Secure", "c=1; Path=/; Secure; Max-Age=0", "d=1; Domain=.other.example; Path=/; Secure");
        String j = CookieDelete.resultJson(p.names(), p);
        require(j.equals("{\"deleted\":[\"a\"],\"skipped\":{\"expired\":1,\"otherHost\":1,\"otherPath\":0,\"filtered\":0}}"),
                "json: " + j);
        require(CookieDelete.resultJson(Collections.<String>emptyList(), new CookieDelete.Plan()).startsWith("{\"deleted\":[]"),
                "empty json");
        require(CookieDelete.resultJson(Arrays.asList("we\"ird\\n"), new CookieDelete.Plan()).contains("we\\\"ird\\\\n"),
                "json escaping");
    }

    private static String names(CookieDelete.Plan p) { return p.names().toString(); }

    private static void require(boolean ok, String msg) {
        if (!ok) throw new AssertionError(msg);
    }
}
