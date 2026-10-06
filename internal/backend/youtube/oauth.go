// oauth.go — YouTube TV OAuth2 device flow ("link with TV code").
//
// YouTube's /player endpoint increasingly gates unauthenticated
// clients behind bot attestation (LOGIN_REQUIRED "Sign in to confirm
// you're not a bot"). An OAuth session bypasses it: the user pairs
// once via google.com/device, the refresh token is persisted through
// the canonical kvstore (DomainSetting — same store the settings
// system uses), and every Innertube request goes out as Bearer
// authenticated — identical to what SmartTube and the real YouTube TV
// app do.
//
// Client credentials are YouTube TV's own public client (embedded in
// https://www.youtube.com/tv's bootstrap script — TV apps ship them,
// they are not per-developer secrets).
package youtube

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/czz/movian-go/internal/db/kvstore"
	facore "github.com/czz/movian-go/internal/fileaccess"
	"github.com/czz/movian-go/internal/misc"
)

const (
	tvClientID     = "861556708454-d6dlm3lh05idd8npek18k6be8ba3oc68.apps.googleusercontent.com"
	tvClientSecret = "SboVhoG9s0rNafixCSGGKXAT"
	tvScope        = "http://gdata.youtube.com " +
		"https://www.googleapis.com/auth/youtube-paid-content"
	deviceCodeURL = "https://oauth2.googleapis.com/device/code"
	tokenURL      = "https://oauth2.googleapis.com/token"
	deviceGrant   = "urn:ietf:params:oauth:grant-type:device_code"
	// kvstore keys under DomainSetting, store "youtube".
	oauthStore    = "youtube"
	oauthRefreshK = "oauth_refresh"
)

// ── Persistence ─────────────────────────────────────────────────────

func (s *System) oauthLoadRefresh() {
	tok := s.kvs.UrlOptGetString(oauthStore, kvstore.DomainSetting,
		oauthRefreshK)
	if tok == "" {
		return
	}
	s.oauth.Lock()
	s.oauth.refreshTok = tok
	s.oauth.Unlock()
	go func() {
		s.refreshAccess()
		s.updateStatus() // settings row → "Signed in" once ready
	}()
}

func (s *System) oauthStoreRefresh(tok string) {
	s.kvs.UrlOptSet(oauthStore, kvstore.DomainSetting, oauthRefreshK,
		kvstore.SetString, tok)
}

func (s *System) oauthClearRefresh() {
	s.kvs.UrlOptSet(oauthStore, kvstore.DomainSetting, oauthRefreshK,
		kvstore.SetVoid, nil)
}

// ── Token state ─────────────────────────────────────────────────────

// accessToken returns a valid Bearer token (refreshing when expired),
// or "" when not signed in.
func (s *System) accessToken() string {
	s.oauth.Lock()
	tok, exp, rt := s.oauth.accessTok, s.oauth.expiresAt, s.oauth.refreshTok
	s.oauth.Unlock()
	if rt == "" {
		return ""
	}
	if tok == "" || time.Now().After(exp.Add(-60*time.Second)) {
		if err := s.refreshAccess(); err != nil {
			s.ts.Error("youtube", "oauth refresh failed: %v", err)
			return tok // use stale token — better than none
		}
		s.oauth.Lock()
		tok = s.oauth.accessTok
		s.oauth.Unlock()
	}
	return tok
}

func (s *System) signedIn() bool {
	s.oauth.Lock()
	defer s.oauth.Unlock()
	return s.oauth.refreshTok != ""
}

// oauthStatus — short string for the settings info prop.
func (s *System) oauthStatus() string {
	s.oauth.Lock()
	defer s.oauth.Unlock()
	switch {
	case s.oauth.signingIn && s.oauth.userCode != "":
		return fmt.Sprintf("Pairing — go to %s and enter code %s",
			s.oauth.verifyURL, s.oauth.userCode)
	case s.oauth.lastErr != "":
		return "Not signed in — last error: " + s.oauth.lastErr
	case s.oauth.refreshTok != "":
		return "Signed in (YouTube account paired)"
	default:
		return "Not signed in — unauthenticated playback may be " +
			"blocked by YouTube (bot check)"
	}
}

// ── Token endpoint ──────────────────────────────────────────────────

type tokenResp struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"`
	Error        string `json:"error"`
}

func (s *System) tokenPost(v url.Values) (*tokenResp, error) {
	body, _, err := s.postForm(tokenURL, v)
	if err != nil {
		return nil, err
	}
	var tr tokenResp
	if err := json.Unmarshal(body, &tr); err != nil {
		return nil, err
	}
	if tr.Error != "" {
		return &tr, fmt.Errorf("token: %s", tr.Error)
	}
	return &tr, nil
}

// postForm POSTs an urlencoded form through the app fileaccess stack.
// FaContentOnError keeps the body on 4xx so OAuth error payloads
// ({"error":"invalid_grant",...}) stay parseable.
func (s *System) postForm(u string, v url.Values) ([]byte, int, error) {
	if s.fam == nil {
		return nil, 0, fmt.Errorf("fileaccess unavailable")
	}
	var q misc.HtsbufQueue
	q.HtsbufQueueSetup(-1)
	q.Append([]byte(v.Encode()))

	var result *misc.Buf
	var errbuf [256]byte
	var code int
	if s.fam.HTTPReq(u,
		facore.HTTPTagResultPtr, &result,
		facore.HTTPTagErrbuf, errbuf[:],
		facore.HTTPTagResponseCode, &code,
		facore.HTTPTagPostData, &q, "application/x-www-form-urlencoded",
		facore.HTTPTagFlags, facore.FaCompression|facore.FaContentOnError) != 0 {
		return nil, code, fmt.Errorf("%s", errbufString(errbuf[:]))
	}
	data := append([]byte(nil), result.C8()[:result.Len()]...)
	result.Release()
	return data, code, nil
}

// refreshAccess exchanges the stored refresh token for a new access
// token. Called on startup (token pre-warm) and lazily on expiry.
func (s *System) refreshAccess() error {
	s.oauth.Lock()
	rt := s.oauth.refreshTok
	s.oauth.Unlock()
	if rt == "" {
		return fmt.Errorf("no refresh token")
	}
	tr, err := s.tokenPost(url.Values{
		"client_id":     {tvClientID},
		"client_secret": {tvClientSecret},
		"refresh_token": {rt},
		"grant_type":    {"refresh_token"},
	})
	if err != nil {
		// refresh token revoked/expired → drop it
		if strings.Contains(err.Error(), "invalid_grant") {
			s.oauthClearRefresh()
			s.oauth.Lock()
			s.oauth.refreshTok, s.oauth.accessTok = "", ""
			s.oauth.Unlock()
		}
		return err
	}
	s.oauth.Lock()
	s.oauth.accessTok = tr.AccessToken
	s.oauth.expiresAt = time.Now().Add(time.Duration(tr.ExpiresIn) * time.Second)
	s.oauth.Unlock()
	s.ts.Info("youtube", "oauth access token refreshed")
	return nil
}

// ── Device flow ─────────────────────────────────────────────────────

// startDeviceFlow kicks off the pairing: requests a device+user code,
// exposes it via s.oauthStatus(), and polls the token endpoint until
// the user approves or the code expires.
func (s *System) startDeviceFlow() {
	s.oauth.Lock()
	if s.oauth.signingIn {
		s.oauth.Unlock()
		return
	}
	s.oauth.signingIn = true
	s.oauth.userCode, s.oauth.verifyURL = "", ""
	s.oauth.lastErr = ""
	s.oauth.Unlock()
	defer func() {
		s.oauth.Lock()
		s.oauth.signingIn = false
		s.oauth.Unlock()
		s.updateStatus()
	}()

	fail := func(format string, args ...any) {
		s.oauth.Lock()
		s.oauth.lastErr = fmt.Sprintf(format, args...)
		s.oauth.Unlock()
		s.ts.Debug("youtube", ""+format, args...)
	}

	raw, _, err := s.postForm(deviceCodeURL, url.Values{
		"client_id": {tvClientID},
		"scope":     {tvScope},
	})
	if err != nil {
		fail("device code request failed: %v", err)
		return
	}
	var dc struct {
		DeviceCode      string `json:"device_code"`
		UserCode        string `json:"user_code"`
		VerificationURL string `json:"verification_url"`
		Interval        int    `json:"interval"`
	}
	if err := json.Unmarshal(raw, &dc); err != nil {
		fail("device code decode failed: %v", err)
		return
	}
	if dc.DeviceCode == "" || dc.UserCode == "" {
		fail("device flow: empty code response")
		return
	}
	s.oauth.Lock()
	s.oauth.userCode, s.oauth.verifyURL = dc.UserCode, dc.VerificationURL
	s.oauth.Unlock()
	s.updateStatus()
	s.ts.Debug("youtube", "pair this device: go to %s and enter %s",
		dc.VerificationURL, dc.UserCode)

	interval := time.Duration(dc.Interval+1) * time.Second
	if interval <= 0 {
		interval = 5 * time.Second
	}
	deadline := time.Now().Add(10 * time.Minute)
	for time.Now().Before(deadline) {
		time.Sleep(interval)
		tr, err := s.tokenPost(url.Values{
			"client_id":     {tvClientID},
			"client_secret": {tvClientSecret},
			"device_code":   {dc.DeviceCode},
			"grant_type":    {deviceGrant},
		})
		if err != nil {
			// authorization_pending → keep polling; anything else → stop
			if tr == nil || (tr.Error != "authorization_pending" &&
				tr.Error != "slow_down") {
				fail("device poll: %v", err)
				return
			}
			continue
		}
		// Approved.
		s.oauth.Lock()
		s.oauth.accessTok = tr.AccessToken
		s.oauth.expiresAt = time.Now().Add(time.Duration(tr.ExpiresIn) * time.Second)
		s.oauth.userCode = ""
		s.oauth.lastErr = ""
		if tr.RefreshToken != "" {
			s.oauth.refreshTok = tr.RefreshToken
			s.oauthStoreRefresh(tr.RefreshToken)
		}
		s.oauth.Unlock()
		s.ts.Debug("youtube", "signed in successfully")
		return
	}
	fail("device pairing timed out")
}

// signOut drops the session: clears tokens and the stored refresh
// token.
func (s *System) signOut() {
	s.oauth.Lock()
	s.oauth.refreshTok, s.oauth.accessTok = "", ""
	s.oauth.userCode = ""
	s.oauth.lastErr = ""
	s.oauth.Unlock()
	s.oauthClearRefresh()
	s.updateStatus()
	s.ts.Debug("youtube", "signed out")
}

// ── Innertube integration ───────────────────────────────────────────

// bearerHeader returns the Authorization header value, or "" when
// not signed in.
func (s *System) bearerHeader() string {
	if t := s.accessToken(); t != "" {
		return "Bearer " + t
	}
	return ""
}
