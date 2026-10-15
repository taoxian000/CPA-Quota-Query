//go:build windows

package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestCompareVersions(t *testing.T) {
	tests := []struct {
		left, right string
		want        int
		wantErr     bool
	}{
		{left: "v1.1", right: "v1.1.0", want: 0},
		{left: "v1.10", right: "v1.9", want: 1},
		{left: "v2.0.0", right: "v2.1", want: -1},
		{left: "v1.1-beta", right: "v1.1", wantErr: true},
		{left: "1.1", right: "v1.1", wantErr: true},
		{left: "v01.1", right: "v1.1", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.left+"_vs_"+tt.right, func(t *testing.T) {
			got, err := compareVersions(tt.left, tt.right)
			if (err != nil) != tt.wantErr {
				t.Fatalf("compareVersions() error = %v, wantErr %v", err, tt.wantErr)
			}
			if err == nil && got != tt.want {
				t.Fatalf("compareVersions() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestSelectReleaseAsset(t *testing.T) {
	data := []byte("verified update")
	digest := sha256.Sum256(data)
	assetName := versionedUpdateAssetName("v1.2")
	valid := githubRelease{
		TagName: "v1.2",
		Assets: []releaseAsset{{
			Name:               assetName,
			BrowserDownloadURL: "https://github.com/taoxian000/CPA-Quota-Query/releases/download/v1.2/" + assetName,
			Digest:             "sha256:" + hex.EncodeToString(digest[:]),
			Size:               int64(len(data)),
		}},
	}
	got, err := selectReleaseAsset(valid)
	if err != nil {
		t.Fatalf("selectReleaseAsset() error = %v", err)
	}
	if got.Tag != "v1.2" || got.Size != int64(len(data)) {
		t.Fatalf("selected release = %+v", got)
	}
	legacy := valid
	legacy.Assets = []releaseAsset{{
		Name:               legacyUpdateAssetName,
		BrowserDownloadURL: "https://github.com/taoxian000/CPA-Quota-Query/releases/download/v1.2/" + legacyUpdateAssetName,
		Digest:             valid.Assets[0].Digest,
		Size:               valid.Assets[0].Size,
	}}
	if _, err := selectReleaseAsset(legacy); err != nil {
		t.Fatalf("selectReleaseAsset() rejected a legacy asset name: %v", err)
	}

	tests := []struct {
		name    string
		release githubRelease
	}{
		{name: "missing exe", release: githubRelease{TagName: "v1.2"}},
		{name: "prerelease", release: githubRelease{TagName: "v1.2", Prerelease: true, Assets: valid.Assets}},
		{name: "invalid tag", release: githubRelease{TagName: "v1.2-rc1", Assets: valid.Assets}},
		{name: "invalid download host", release: githubRelease{TagName: "v1.2", Assets: []releaseAsset{{Name: assetName, BrowserDownloadURL: "https://example.com/app.exe", Digest: valid.Assets[0].Digest, Size: valid.Assets[0].Size}}}},
		{name: "missing digest", release: githubRelease{TagName: "v1.2", Assets: []releaseAsset{{Name: assetName, BrowserDownloadURL: valid.Assets[0].BrowserDownloadURL, Size: valid.Assets[0].Size}}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := selectReleaseAsset(tt.release); err == nil {
				t.Fatal("selectReleaseAsset() expected an error")
			}
		})
	}
}

func TestFetchLatestRelease(t *testing.T) {
	data := []byte("release payload")
	digest := sha256.Sum256(data)
	assetName := versionedUpdateAssetName("v1.2")
	encoded, err := json.Marshal(githubRelease{
		TagName: "v1.2",
		Assets: []releaseAsset{{
			Name:               assetName,
			BrowserDownloadURL: "https://github.com/taoxian000/CPA-Quota-Query/releases/download/v1.2/" + assetName,
			Digest:             "sha256:" + hex.EncodeToString(digest[:]),
			Size:               int64(len(data)),
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Accept") != "application/vnd.github+json" {
			t.Errorf("Accept header = %q", r.Header.Get("Accept"))
		}
		_, _ = w.Write(encoded)
	}))
	defer server.Close()
	release, err := fetchLatestRelease(server.Client(), server.URL)
	if err != nil {
		t.Fatalf("fetchLatestRelease() error = %v", err)
	}
	if release.Tag != "v1.2" {
		t.Fatalf("release tag = %q", release.Tag)
	}

	missing := httptest.NewServer(http.NotFoundHandler())
	defer missing.Close()
	if _, err := fetchLatestRelease(missing.Client(), missing.URL); !errors.Is(err, errNoRelease) {
		t.Fatalf("404 error = %v, want errNoRelease", err)
	}
}

func TestDownloadAndVerifyChecksDigestAndSize(t *testing.T) {
	data := []byte("executable test payload")
	digest := sha256.Sum256(data)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(data)
	}))
	defer server.Close()

	release := updateRelease{URL: server.URL, Digest: hex.EncodeToString(digest[:]), Size: int64(len(data))}
	path, err := downloadAndVerify(server.Client(), release)
	if err != nil {
		t.Fatalf("downloadAndVerify() error = %v", err)
	}
	t.Cleanup(func() { _ = os.Remove(path) })
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(data) {
		t.Fatalf("downloaded data = %q, want %q", got, data)
	}

	wrongDigest := release
	wrongDigest.Digest = strings.Repeat("0", 64)
	if _, err := downloadAndVerify(server.Client(), wrongDigest); err == nil || !strings.Contains(err.Error(), "SHA-256") {
		t.Fatalf("mismatch error = %v, want SHA-256 error", err)
	}
	wrongSize := release
	wrongSize.Size++
	if _, err := downloadAndVerify(server.Client(), wrongSize); err == nil || !strings.Contains(err.Error(), "大小") {
		t.Fatalf("size mismatch error = %v, want size error", err)
	}
}

func TestPowershellStringSurvivesQuoteCharacters(t *testing.T) {
	powershell, err := exec.LookPath("powershell.exe")
	if err != nil {
		t.Skip("Windows PowerShell is not available")
	}
	// ’ (U+2019) ends a single-quoted PowerShell string just like '.
	value := `C:\Users\O’Brien's ‘app’\quota-monitor.exe`
	script := "[Console]::OutputEncoding = [System.Text.Encoding]::UTF8\n[Console]::Out.Write(" + powershellString(value) + ")"
	output, err := exec.Command(powershell, "-NoProfile", "-NonInteractive", "-EncodedCommand", encodePowerShellCommand(script)).Output()
	if err != nil {
		t.Fatalf("PowerShell rejected the generated expression: %v", err)
	}
	if got := strings.TrimPrefix(string(output), "\uFEFF"); got != value {
		t.Fatalf("PowerShell decoded %q, want %q", got, value)
	}

	// The update helper only runs during a real update, so at least make sure
	// PowerShell parses it without syntax errors.
	helper := updateHelperScript(value, `C:\Temp\update.exe`, strings.Repeat("ab", 32), 1234)
	check := "$errors = $null\n[void][System.Management.Automation.Language.Parser]::ParseInput(" + powershellString(helper) + ", [ref]$null, [ref]$errors)\n[Console]::Out.Write($errors.Count)"
	output, err = exec.Command(powershell, "-NoProfile", "-NonInteractive", "-EncodedCommand", encodePowerShellCommand(check)).Output()
	if err != nil {
		t.Fatalf("PowerShell could not parse the update helper: %v", err)
	}
	if got := strings.TrimSpace(string(output)); got != "0" {
		t.Fatalf("update helper script has %s PowerShell syntax error(s)", got)
	}
}

func TestUpdateHelperScriptVerifiesDigestWithoutRawPaths(t *testing.T) {
	target := `C:\Apps\O’Brien\quota-monitor.exe`
	digest := strings.Repeat("ab", 32)
	script := updateHelperScript(target, `C:\Temp\update.exe`, digest, 1234)
	if strings.Contains(script, "O’Brien") || strings.Contains(script, `C:\Temp`) {
		t.Fatal("helper script embeds a raw path instead of an encoded string")
	}
	for _, want := range []string{"Get-FileHash -LiteralPath $staging -Algorithm SHA256", powershellString(digest), "Wait-Process -Id 1234"} {
		if !strings.Contains(script, want) {
			t.Fatalf("helper script is missing %q", want)
		}
	}
}

func TestFetchQuotaRetriesFiveTimes(t *testing.T) {
	attempts := 0
	var delays []time.Duration
	want := quotaSnapshot{Nominal: []accountQuota{{Email: "nominal@example.test"}}}
	got, err := fetchQuotaWithRetry(func() (quotaSnapshot, error) {
		attempts++
		if attempts <= quotaRequestRetries {
			return quotaSnapshot{}, errors.New("temporary network error")
		}
		return want, nil
	}, func(delay time.Duration) {
		delays = append(delays, delay)
	})
	if err != nil {
		t.Fatalf("fetchQuotaWithRetry() error = %v", err)
	}
	if attempts != quotaRequestRetries+1 {
		t.Fatalf("request attempts = %d, want %d", attempts, quotaRequestRetries+1)
	}
	if got.Nominal[0].Email != want.Nominal[0].Email {
		t.Fatalf("quota result = %+v, want %+v", got, want)
	}
	wantDelays := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 8 * time.Second}
	if len(delays) != len(wantDelays) {
		t.Fatalf("retry delays = %v, want %v", delays, wantDelays)
	}
	for i := range wantDelays {
		if delays[i] != wantDelays[i] {
			t.Fatalf("retry delays = %v, want %v", delays, wantDelays)
		}
	}
}

func TestFetchQuotaStopsRetryingOnClientErrors(t *testing.T) {
	for _, code := range []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusFound} {
		attempts := 0
		_, err := fetchQuotaWithRetry(func() (quotaSnapshot, error) {
			attempts++
			return quotaSnapshot{}, &httpStatusError{operation: "额度接口", code: code}
		}, func(time.Duration) {})
		if attempts != 1 {
			t.Fatalf("HTTP %d was attempted %d times, want 1", code, attempts)
		}
		if err == nil || strings.Contains(err.Error(), "连续失败") {
			t.Fatalf("HTTP %d error = %v, want the plain status error", code, err)
		}
	}

	attempts := 0
	_, err := fetchQuotaWithRetry(func() (quotaSnapshot, error) {
		attempts++
		if attempts == 1 {
			return quotaSnapshot{}, &httpStatusError{operation: "额度接口", code: http.StatusServiceUnavailable}
		}
		return quotaSnapshot{}, &httpStatusError{operation: "额度接口", code: http.StatusUnauthorized}
	}, func(time.Duration) {})
	if attempts != 2 || !strings.Contains(err.Error(), "共尝试 2 次") {
		t.Fatalf("503 then 401: attempts = %d, error = %v", attempts, err)
	}
	if got := quotaFailureCause(err).Error(); got != "额度接口返回 HTTP 401" {
		t.Fatalf("failure cause = %q, want the last status error", got)
	}
}

func TestQuotaClientDoesNotFollowRedirects(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("credentials were forwarded to the redirect target")
	}))
	defer target.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	defer server.Close()
	_, err := fetchQuotaAt(newQuotaClient(nil), server.URL, "cpa-key")
	var statusErr *httpStatusError
	if !errors.As(err, &statusErr) || statusErr.code != http.StatusFound {
		t.Fatalf("redirect error = %v, want HTTP 302 status error", err)
	}
}

func TestQuotaEndpointsFor(t *testing.T) {
	tests := []struct {
		name      string
		serverURL string
		want      string
		wantErr   bool
	}{
		{name: "default", serverURL: "", want: defaultServerURL + quotaRoutePath},
		{name: "custom host", serverURL: "https://cpa.example.test/", want: "https://cpa.example.test" + quotaRoutePath},
		{name: "path prefix", serverURL: " https://example.test:8443/cpa ", want: "https://example.test:8443/cpa" + quotaRoutePath},
		{name: "loopback http", serverURL: "http://127.0.0.1:8317", want: "http://127.0.0.1:8317" + quotaRoutePath},
		{name: "localhost http", serverURL: "http://localhost:8317", want: "http://localhost:8317" + quotaRoutePath},
		{name: "remote http", serverURL: "http://cpa.example.test", wantErr: true},
		{name: "missing scheme", serverURL: "cpa.example.test", wantErr: true},
		{name: "query", serverURL: "https://cpa.example.test/?x=1", wantErr: true},
		{name: "userinfo", serverURL: "https://user:pass@cpa.example.test", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := quotaEndpointsFor(tt.serverURL)
			if (err != nil) != tt.wantErr {
				t.Fatalf("quotaEndpointsFor(%q) error = %v, wantErr %v", tt.serverURL, err, tt.wantErr)
			}
			if err != nil {
				return
			}
			if got.quota != tt.want || got.direct != tt.want+"/direct" || got.reset != tt.want+"/reset" || got.directReset != tt.want+"/direct/reset" {
				t.Fatalf("quotaEndpointsFor(%q) = %+v, want base %q", tt.serverURL, got, tt.want)
			}
		})
	}
}

func TestParseQuotaCredentialsByAuthMode(t *testing.T) {
	tests := []struct {
		name    string
		payload string
		mode    string
		key     string
		token   string
		account string
		wantErr bool
	}{
		{name: "apikey mode", payload: `{"auth_mode":"apikey","OPENAI_API_KEY":"cpa-key"}`, mode: authModeAPIKey, key: "cpa-key"},
		{name: "chatgpt mode", payload: `{"auth_mode":"chatgpt","OPENAI_API_KEY":null,"tokens":{"access_token":"access-token","account_id":"account-uuid"}}`, mode: authModeChatGPT, token: "access-token", account: "account-uuid"},
		{name: "legacy api key without mode", payload: `{"OPENAI_API_KEY":"legacy-key"}`, mode: authModeAPIKey, key: "legacy-key"},
		{name: "apikey missing key", payload: `{"auth_mode":"apikey","OPENAI_API_KEY":null}`, wantErr: true},
		{name: "chatgpt missing account", payload: `{"auth_mode":"chatgpt","tokens":{"access_token":"access-token"}}`, wantErr: true},
		{name: "unsupported mode", payload: `{"auth_mode":"other","OPENAI_API_KEY":"key"}`, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseQuotaCredentials([]byte(tt.payload))
			if (err != nil) != tt.wantErr {
				t.Fatalf("parseQuotaCredentials() error state = %v, want error %v", err != nil, tt.wantErr)
			}
			if err != nil {
				return
			}
			if got.Mode != tt.mode || got.APIKey != tt.key || got.AccessToken != tt.token || got.AccountID != tt.account {
				t.Fatal("parsed auth mode or credentials did not match the fixture")
			}
		})
	}
}

func TestFetchQuotaUsingAuthRoutesByMode(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/apikey":
			if r.Method != http.MethodGet || r.Header.Get("Authorization") != "Bearer cpa-key" {
				t.Error("apikey request method or authorization header was incorrect")
			}
			_, _ = w.Write([]byte(`{"nominal_accounts":{"nominal@example.test":{"groups":[{"displayName":"Codex","buckets":[{"window":"primary","remainingFraction":0.8}]}]}}}`))
		case "/direct":
			if r.Method != http.MethodGet || r.Header.Get("Authorization") != "Bearer access-token" || r.Header.Get("ChatGPT-Account-ID") != "account-uuid" {
				t.Error("ChatGPT direct request method or credential headers were incorrect")
			}
			_, _ = w.Write([]byte(`{"accounts":{"chatgpt@example.test":{"groups":[{"displayName":"Codex","buckets":[{"window":"primary","remainingFraction":0.65}]}],"reset_credits":{"available_count":2,"expires_at":[],"expiry_details_available":true,"expiry_details_complete":true}}}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	tests := []struct {
		name        string
		credentials quotaCredentials
		wantEmail   string
		wantDirect  bool
	}{
		{name: "apikey", credentials: quotaCredentials{Mode: authModeAPIKey, APIKey: "cpa-key"}, wantEmail: "nominal@example.test"},
		{name: "chatgpt", credentials: quotaCredentials{Mode: authModeChatGPT, AccessToken: "access-token", AccountID: "account-uuid"}, wantEmail: "chatgpt@example.test", wantDirect: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := fetchQuotaUsingAuth(server.Client(), tt.credentials, server.URL+"/apikey", server.URL+"/direct")
			if err != nil {
				t.Fatalf("fetchQuotaUsingAuth() error = %v", err)
			}
			if len(got.Nominal) != 1 || got.Nominal[0].Email != tt.wantEmail || got.DirectMode != tt.wantDirect {
				t.Fatalf("routed quota result did not match expected account or mode")
			}
			if !got.ResetSupported {
				t.Fatalf("quota reset should be supported for direct mode %v", tt.wantDirect)
			}
			if tt.wantDirect && (got.Nominal[0].Primary.Percent != 65 || got.Nominal[0].Reset.AvailableCount != 2 || len(got.Actual) != 0 || !got.singleAccount()) {
				t.Fatal("direct response quota or reset-credit data was not parsed correctly")
			}
		})
	}
}

func TestParseAccountsKeepsEveryAccountInServerOrder(t *testing.T) {
	raw := json.RawMessage(`{"zeta@example.test":{"reset_credits":{"available_count":1}},"alpha@example.test":{"reset_credits":{"available_count":2}}}`)
	accounts := parseAccounts(raw)
	if len(accounts) != 2 || accounts[0].Email != "zeta@example.test" || accounts[1].Email != "alpha@example.test" {
		t.Fatalf("parsed accounts = %+v, want both accounts in response order", accounts)
	}
	if availableResetCredits(accounts) != 3 {
		t.Fatalf("available reset credits = %d, want 3 across all accounts", availableResetCredits(accounts))
	}

	direct, err := parseDirectQuotaEnvelope(directResponseEnvelope{Accounts: raw})
	if err != nil || len(direct.Nominal) != 2 || direct.Nominal[0].Email != "zeta@example.test" || direct.singleAccount() {
		t.Fatalf("direct envelope = %+v, %v; want both accounts in response order", direct, err)
	}
	if _, err := parseDirectQuotaEnvelope(directResponseEnvelope{Accounts: json.RawMessage(`{}`)}); err == nil {
		t.Fatal("an empty direct response should be an error")
	}
}

func TestTrayAccountUsesDirectChatGPTQuota(t *testing.T) {
	chatGPTAccount := accountQuota{Email: "chatgpt@example.test", Primary: quotaBucket{Percent: 62, Known: true}}
	got := trayAccount(quotaSnapshot{Nominal: []accountQuota{chatGPTAccount}, DirectMode: true})
	if got.Email != chatGPTAccount.Email || got.Primary.Percent != chatGPTAccount.Primary.Percent {
		t.Fatal("tray icon did not use the direct ChatGPT account quota")
	}

	got = trayAccount(quotaSnapshot{Nominal: []accountQuota{chatGPTAccount}})
	if got.Email != chatGPTAccount.Email || got.Primary.Percent != chatGPTAccount.Primary.Percent {
		t.Fatal("tray icon did not use the only nominal account when actual_accounts is absent")
	}

	actualAccount := accountQuota{Email: "actual@example.test", Primary: quotaBucket{Percent: 37, Known: true}}
	got = trayAccount(quotaSnapshot{Nominal: []accountQuota{chatGPTAccount}, Actual: []accountQuota{actualAccount}})
	if got.Email != actualAccount.Email || got.Primary.Percent != actualAccount.Primary.Percent {
		t.Fatal("tray icon did not prefer actual-account quota in API-key mode")
	}
}

func TestRequestQuotaResetRoutesByAuthMode(t *testing.T) {
	calls := map[string]int{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		calls[r.URL.Path]++
		if r.Method != http.MethodGet {
			t.Error("quota reset request method was incorrect")
		}
		switch r.URL.Path {
		case "/reset":
			if r.Header.Get("Authorization") != "Bearer cpa-key" {
				t.Error("API-key reset request used an incorrect authorization header")
			}
			_, _ = w.Write([]byte(`{"success":true,"nominal_accounts":{"api@example.test":{"success":true,"message":"Codex quota reset credit consumed"}}}`))
		case "/direct/reset":
			if r.Header.Get("Authorization") != "Bearer access-token" || r.Header.Get("ChatGPT-Account-ID") != "account-uuid" || r.Header.Get("User-Agent") != userAgent() {
				t.Error("direct reset request headers did not match the direct quota request")
			}
			_, _ = w.Write([]byte(`{"success":true,"accounts":{"direct@example.test":{"success":true,"message":"Codex quota reset credit consumed"}}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	tests := []struct {
		name        string
		credentials quotaCredentials
		wantEmail   string
	}{
		{name: "apikey", credentials: quotaCredentials{Mode: authModeAPIKey, APIKey: "cpa-key"}, wantEmail: "api@example.test"},
		{name: "chatgpt", credentials: quotaCredentials{Mode: authModeChatGPT, AccessToken: "access-token", AccountID: "account-uuid"}, wantEmail: "direct@example.test"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := requestQuotaResetWithAuth(server.Client(), tt.credentials, server.URL+"/reset", server.URL+"/direct/reset")
			if err != nil {
				t.Fatalf("requestQuotaResetWithAuth() error = %v", err)
			}
			if !strings.Contains(got, tt.wantEmail+"：成功") {
				t.Fatalf("reset response did not include success for the expected account")
			}
		})
	}
	if calls["/reset"] != 1 || calls["/direct/reset"] != 1 {
		t.Fatalf("reset endpoints called counts = %#v, want one call each (no retries)", calls)
	}
}

func TestResetConfirmationDescribesEveryNominalAccount(t *testing.T) {
	single := resetConfirmation(quotaSnapshot{Nominal: []accountQuota{{Email: "one@example.test"}}})
	if !strings.Contains(single, "one@example.test") || strings.Contains(single, "你已说明") {
		t.Fatalf("single-account confirmation = %q", single)
	}
	multiple := resetConfirmation(quotaSnapshot{Nominal: []accountQuota{{Email: "one@example.test"}, {Email: "two@example.test"}}})
	if !strings.Contains(multiple, "共 2 个账户") || !strings.Contains(multiple, "one@example.test\ntwo@example.test") {
		t.Fatalf("multi-account confirmation = %q", multiple)
	}
	direct := resetConfirmation(quotaSnapshot{Nominal: []accountQuota{{Email: "direct@example.test"}}, DirectMode: true})
	if !strings.Contains(direct, "当前 Codex 认证") || !strings.Contains(direct, "direct@example.test") {
		t.Fatalf("direct confirmation = %q", direct)
	}
}

func TestNominalAccountTitleForSingleAccount(t *testing.T) {
	if got := nominalAccountTitle(false, true); got != "账户" {
		t.Fatalf("single-account title = %q, want 账户", got)
	}
	if got := nominalAccountTitle(false, false); got != "名义账户" {
		t.Fatalf("multi-account nominal title = %q, want 名义账户", got)
	}
	if got := nominalAccountTitle(true, false); got != "ChatGPT 账户" {
		t.Fatalf("non-single direct title = %q, want ChatGPT 账户", got)
	}
}

func TestParseQuotaEnvelopeMarksSingleAccount(t *testing.T) {
	var single responseEnvelope
	if err := json.Unmarshal([]byte(`{"nominal_accounts":{"single@example.test":{"groups":[]}}}`), &single); err != nil {
		t.Fatal(err)
	}
	got := parseQuotaEnvelope(single)
	if !got.singleAccount() || len(got.Nominal) != 1 || len(got.Actual) != 0 || !got.ResetSupported {
		t.Fatal("single nominal account was not identified")
	}

	var multiple responseEnvelope
	if err := json.Unmarshal([]byte(`{"nominal_accounts":{"nominal@example.test":{"groups":[]}},"actual_accounts":{"actual@example.test":{"groups":[]}}}`), &multiple); err != nil {
		t.Fatal(err)
	}
	got = parseQuotaEnvelope(multiple)
	if got.singleAccount() || len(got.Actual) != 1 {
		t.Fatal("nominal-plus-actual response was incorrectly identified as a single account")
	}
}

// TestAuthFixtureSelectsQuotaRouteWithoutExposingCredentials is an opt-in
// smoke test against a real Codex auth file, kept out of normal runs so tests
// never depend on (or read) the developer's credentials by default.
func TestAuthFixtureSelectsQuotaRouteWithoutExposingCredentials(t *testing.T) {
	path := os.Getenv("CPA_QUOTA_AUTH_FIXTURE")
	if path == "" {
		t.Skip("set CPA_QUOTA_AUTH_FIXTURE to a copy of a Codex auth.json to run this smoke test")
	}
	credentials, err := readQuotaCredentialsFromPath(path)
	if err != nil {
		t.Fatalf("could not parse auth fixture: %v", err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if credentials.Mode == authModeChatGPT {
			if (r.URL.Path != "/direct" && r.URL.Path != "/direct/reset") ||
				r.Header.Get("Authorization") != "Bearer "+credentials.AccessToken ||
				r.Header.Get("ChatGPT-Account-ID") != credentials.AccountID {
				t.Error("auth fixture credentials were not routed to the direct endpoint correctly")
			}
			if r.URL.Path == "/direct/reset" {
				_, _ = w.Write([]byte(`{"success":true,"accounts":{"fixture@example.test":{"success":true}}}`))
				return
			}
			_, _ = w.Write([]byte(`{"accounts":{"fixture@example.test":{"groups":[]}}}`))
			return
		}
		if (r.URL.Path != "/apikey" && r.URL.Path != "/reset") || r.Header.Get("Authorization") != "Bearer "+credentials.APIKey {
			t.Error("auth fixture API key was not routed to the original endpoint correctly")
		}
		if r.URL.Path == "/reset" {
			_, _ = w.Write([]byte(`{"success":true,"nominal_accounts":{"fixture@example.test":{"success":true}}}`))
			return
		}
		_, _ = w.Write([]byte(`{"nominal_accounts":{"fixture@example.test":{"groups":[]}}}`))
	}))
	defer server.Close()

	got, err := fetchQuotaUsingAuth(server.Client(), credentials, server.URL+"/apikey", server.URL+"/direct")
	if err != nil {
		t.Fatalf("auth fixture route smoke test failed: %v", err)
	}
	if got.DirectMode != (credentials.Mode == authModeChatGPT) || len(got.Nominal) != 1 || got.Nominal[0].Email != "fixture@example.test" {
		t.Fatal("auth fixture selected an unexpected quota route")
	}
	resetResult, err := requestQuotaResetWithAuth(server.Client(), credentials, server.URL+"/reset", server.URL+"/direct/reset")
	if err != nil || !strings.Contains(resetResult, "fixture@example.test：成功") {
		t.Fatal("auth fixture did not route reset through the local mock correctly")
	}
}

func TestParseAccountReadsResetCreditDetails(t *testing.T) {
	raw := json.RawMessage(`{"nominal@example.test":{"groups":[{"displayName":"Codex","buckets":[{"window":"primary","remainingFraction":0.75,"resetTime":"2026-09-24T15:00:00Z"},{"window":"secondary","remainingFraction":0.5,"reset_time":"2026-09-29T10:00:00Z"}]}],"reset_credits":{"available_count":2,"expires_at":["2026-09-24T12:00:00Z"],"without_expiry":1,"expiry_details_available":true,"expiry_details_complete":false}}}`)
	accounts := parseAccounts(raw)
	if len(accounts) != 1 {
		t.Fatalf("parsed %d accounts, want 1", len(accounts))
	}
	account := accounts[0]
	if account.Email != "nominal@example.test" {
		t.Fatalf("email = %q", account.Email)
	}
	if !account.Primary.Known || account.Primary.Percent != 75 {
		t.Fatalf("primary quota = %+v", account.Primary)
	}
	if got := account.Primary.ResetAt.Format(time.RFC3339); got != "2026-09-24T15:00:00Z" {
		t.Fatalf("primary reset time = %q", got)
	}
	if got := account.Secondary.ResetAt.Format(time.RFC3339); got != "2026-09-29T10:00:00Z" {
		t.Fatalf("secondary reset time = %q", got)
	}
	if account.Reset.AvailableCount != 2 || account.Reset.WithoutExpiry != 1 {
		t.Fatalf("reset count data = %+v", account.Reset)
	}
	if len(account.Reset.ExpiresAt) != 1 || !account.Reset.ExpiryDetailsAvailable || account.Reset.ExpiryDetailsComplete {
		t.Fatalf("reset expiry data = %+v", account.Reset)
	}
}

func TestParseQuotaEnvelopeTracksActualAccountsFieldPresence(t *testing.T) {
	tests := []struct {
		name      string
		payload   string
		available bool
	}{
		{name: "field omitted", payload: `{"nominal_accounts":{}}`, available: false},
		{name: "field present with account data", payload: `{"actual_accounts":{"actual@example.test":{}}}`, available: true},
		{name: "empty object", payload: `{"actual_accounts":{}}`, available: false},
		{name: "null", payload: `{"actual_accounts":null}`, available: false},
		{name: "empty string", payload: `{"actual_accounts":""}`, available: false},
		{name: "empty array", payload: `{"actual_accounts":[]}`, available: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var envelope responseEnvelope
			if err := json.Unmarshal([]byte(tt.payload), &envelope); err != nil {
				t.Fatal(err)
			}
			got := parseQuotaEnvelope(envelope)
			if (len(got.Actual) > 0) != tt.available {
				t.Fatalf("actual accounts = %d, want available %v", len(got.Actual), tt.available)
			}
		})
	}
}

func TestPanelHeightOmitsActualCardWhenFieldIsMissing(t *testing.T) {
	withoutActual := panelHeightFor(quotaSnapshot{})
	withActual := panelHeightFor(quotaSnapshot{Actual: []accountQuota{{}}})
	if difference := withActual - withoutActual; difference != panelCardGap+actualCardHeight {
		t.Fatalf("actual card adds %d px, want %d px", difference, panelCardGap+actualCardHeight)
	}
}

func TestPanelLayoutStacksEveryAccount(t *testing.T) {
	quota := quotaSnapshot{
		Nominal:        []accountQuota{{Email: "one@example.test"}, {Email: "two@example.test"}},
		Actual:         []accountQuota{{Email: "actual@example.test"}},
		ResetSupported: true,
	}
	cards, height := panelLayout(quota)
	if len(cards) != 3 || !cards[0].nominal || !cards[1].nominal || cards[2].nominal {
		t.Fatalf("cards = %+v, want two nominal cards then one actual card", cards)
	}
	if !cards[0].resetButton || cards[1].resetButton {
		t.Fatal("only the first nominal card should carry the reset button")
	}
	for i := 1; i < len(cards); i++ {
		if cards[i].top != cards[i-1].top+cards[i-1].height+panelCardGap {
			t.Fatalf("card %d top = %d, want it directly below card %d", i, cards[i].top, i-1)
		}
	}
	last := cards[len(cards)-1]
	if height != last.top+last.height+panelBottomMargin {
		t.Fatalf("panel height = %d, want bottom margin below the last card", height)
	}

	placeholder, _ := panelLayout(quotaSnapshot{})
	if len(placeholder) != 1 || !placeholder[0].nominal || placeholder[0].resetButton {
		t.Fatal("before the first sync the panel should show one empty nominal card without a reset button")
	}
}

func TestQuotaRefreshAddsActualAccountCardToSnapshot(t *testing.T) {
	var initial responseEnvelope
	if err := json.Unmarshal([]byte(`{"nominal_accounts":{"nominal@example.test":{"groups":[]}}}`), &initial); err != nil {
		t.Fatal(err)
	}
	opened := parseQuotaEnvelope(initial)
	if len(opened.Actual) != 0 {
		t.Fatal("actual-account card should be hidden when the server omits actual_accounts")
	}

	var refreshed responseEnvelope
	if err := json.Unmarshal([]byte(`{"nominal_accounts":{"nominal@example.test":{"groups":[]}},"actual_accounts":{"actual@example.test":{"groups":[]}}}`), &refreshed); err != nil {
		t.Fatal(err)
	}
	afterRefresh := parseQuotaEnvelope(refreshed)
	if len(afterRefresh.Actual) != 1 {
		t.Fatal("actual-account card should appear when a refreshed response contains actual_accounts")
	}
	if panelHeightFor(afterRefresh) <= panelHeightFor(opened) {
		t.Fatal("window height should grow after refresh adds the actual-account card")
	}
}

func TestScaleForDPI(t *testing.T) {
	for _, tt := range []struct{ value, dpi, want int32 }{
		{540, 96, 540}, {540, 120, 675}, {540, 144, 810}, {540, 192, 1080}, {17, 144, 26}, {540, 0, 540},
	} {
		if got := scaleForDPI(tt.value, tt.dpi); got != tt.want {
			t.Fatalf("scaleForDPI(%d, %d) = %d, want %d", tt.value, tt.dpi, got, tt.want)
		}
	}
	app := &monitorApp{dpi: 144}
	if got := app.logicalPoint(point{X: 810, Y: 150}); got != (point{X: 540, Y: 100}) {
		t.Fatalf("logicalPoint() = %+v, want {540 100}", got)
	}
}

func TestClampToWorkArea(t *testing.T) {
	work := rect{Left: 0, Top: 0, Right: 1920, Bottom: 1040}
	if x, y := clampToWorkArea(1800, 900, 540, 300, work); x != 1380 || y != 740 {
		t.Fatalf("clamp bottom-right = (%d, %d), want (1380, 740)", x, y)
	}
	if x, y := clampToWorkArea(-20, -5, 540, 300, work); x != 0 || y != 0 {
		t.Fatalf("clamp top-left = (%d, %d), want (0, 0)", x, y)
	}
	if _, y := clampToWorkArea(0, 500, 540, 1200, work); y != 0 {
		t.Fatalf("a panel taller than the work area should keep its top edge visible, y = %d", y)
	}
}

func TestTrayClickAfterAutoHideClosesPanel(t *testing.T) {
	app := &monitorApp{}
	if app.trayClickHidesPanel() {
		t.Fatal("a click on the tray icon should open a panel that has been hidden for a while")
	}
	app.panelAutoHiddenAt = time.Now()
	if !app.trayClickHidesPanel() {
		t.Fatal("the click that auto-hid the panel must not reopen it")
	}
	app.panelAutoHiddenAt = time.Now().Add(-2 * trayToggleGrace)
	app.panelVisible = true
	if !app.trayClickHidesPanel() {
		t.Fatal("a click on the tray icon should close a visible panel")
	}
}

func TestTrayIconPixelsArePixelAlignedAtEverySize(t *testing.T) {
	account := accountQuota{Primary: quotaBucket{Percent: 100, Known: true}, Secondary: quotaBucket{Percent: 40, Known: true}}
	for _, size := range []int{16, 20, 24, 28, 32, 40, 48, 64} {
		pixels := make([]byte, size*size*4)
		drawTrayIconPixels(pixels, size, account)
		opaque := 0
		for i := 3; i < len(pixels); i += 4 {
			switch pixels[i] {
			case 0:
			case 255:
				opaque++
			default:
				t.Fatalf("size %d: pixel alpha %d; icon pixels must be fully opaque or transparent", size, pixels[i])
			}
		}
		if opaque < size*size/4 {
			t.Fatalf("size %d: only %d opaque pixels were drawn", size, opaque)
		}
		// The 100% bar is wider than the 40% bar.
		full, partial := colorPixels(pixels, quotaColor(100)), colorPixels(pixels, quotaColor(40))
		if partial == 0 || full <= partial {
			t.Fatalf("size %d: 100%% bar has %d pixels, 40%% bar has %d", size, full, partial)
		}
	}
}

func colorPixels(pixels []byte, color uint32) int {
	count := 0
	for i := 0; i < len(pixels); i += 4 {
		if pixels[i] == byte(color>>16) && pixels[i+1] == byte(color>>8) && pixels[i+2] == byte(color) && pixels[i+3] == 255 {
			count++
		}
	}
	return count
}

func TestUTF16TextDropsNULCharacters(t *testing.T) {
	got := utf16Text("a\x00b")
	if len(got) != 3 || got[0] != 'a' || got[1] != 'b' || got[2] != 0 {
		t.Fatalf("utf16Text() = %v, want \"ab\" with a terminator", got)
	}
	if p := utf16Ptr("\x00"); p == nil || *p != 0 {
		t.Fatal("utf16Ptr() must return an empty string, not nil, for text that is only NULs")
	}
}

func TestQuotaRemainingTextFormatsHoursDaysAndMinutes(t *testing.T) {
	now := time.Date(2026, 9, 24, 10, 0, 0, 0, time.UTC)
	tests := []struct {
		name string
		at   time.Time
		want string
	}{
		{name: "hours and minutes", at: now.Add(4*time.Hour + 45*time.Minute), want: "剩余时间：4小时45分"},
		{name: "days and hours", at: now.Add(6*24*time.Hour + 3*time.Hour + 20*time.Minute), want: "剩余时间：6天3小时"},
		{name: "minutes under an hour", at: now.Add(42 * time.Minute), want: "剩余时间：42分"},
		{name: "sub-minute rounds up", at: now.Add(30 * time.Second), want: "剩余时间：1分"},
		{name: "expired", at: now.Add(-time.Minute), want: "剩余时间：已到期"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := quotaRemainingText(quotaBucket{ResetAt: tt.at}, now)
			if got != tt.want {
				t.Fatalf("quotaRemainingText() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestNextQuotaExpiryFindsEarliestQuotaOrResetCreditExpiry(t *testing.T) {
	now := time.Date(2026, 10, 10, 10, 0, 0, 0, time.UTC)
	quota := quotaSnapshot{
		Nominal: []accountQuota{{
			Primary:   quotaBucket{ResetAt: now.Add(3 * time.Hour)},
			Secondary: quotaBucket{ResetAt: now.Add(time.Hour)},
		}, {
			Reset: resetCreditInfo{ExpiresAt: []string{now.Add(30 * time.Minute).Format(time.RFC3339)}},
		}},
		Actual: []accountQuota{{
			Primary: quotaBucket{ResetAt: now.Add(2 * time.Hour)},
		}},
	}

	next, expired := nextQuotaExpiry(quota, now, time.Time{})
	if expired {
		t.Fatal("future quota expiries should not be considered expired")
	}
	if want := now.Add(30 * time.Minute); !next.Equal(want) {
		t.Fatalf("next quota expiry = %v, want earliest expiry %v", next, want)
	}
}

func TestNextQuotaExpiryRequestsRefreshOnceAnyExpiryHasPassed(t *testing.T) {
	now := time.Date(2026, 10, 10, 10, 0, 0, 0, time.UTC)
	quota := quotaSnapshot{
		Nominal: []accountQuota{{Primary: quotaBucket{ResetAt: now.Add(-time.Second)}}},
		Actual:  []accountQuota{{Secondary: quotaBucket{ResetAt: now.Add(time.Hour)}}},
	}
	if _, expired := nextQuotaExpiry(quota, now, time.Time{}); !expired {
		t.Fatal("an expired quota bucket should request an immediate refresh")
	}
	next, expired := nextQuotaExpiry(quota, now, now)
	if expired {
		t.Fatal("an expiry already handled by the forced refresh should not trigger again")
	}
	if want := now.Add(time.Hour); !next.Equal(want) {
		t.Fatalf("next unhandled expiry = %v, want %v", next, want)
	}
}

func TestQuotaRecoveryTrackerAnnouncesExhaustedWindowsOnce(t *testing.T) {
	now := time.Date(2026, 10, 15, 10, 0, 0, 0, time.UTC)
	primaryReset := now.Add(time.Hour)
	weeklyReset := now.Add(48 * time.Hour)
	exhausted := []accountQuota{{
		Email:     "nominal@example.test",
		Primary:   quotaBucket{Known: true, Percent: 0, ResetAt: primaryReset},
		Secondary: quotaBucket{Known: true, Percent: 0, ResetAt: weeklyReset},
	}}
	tracker := newQuotaRecoveryTracker()
	if notices := tracker.update(exhausted, now); len(notices) != 0 {
		t.Fatalf("notices before the reset time = %+v", notices)
	}
	if due := tracker.nextDue(); !due.Equal(primaryReset) {
		t.Fatalf("next due = %v, want the 5h reset %v", due, primaryReset)
	}

	notices := tracker.update(exhausted, primaryReset)
	if len(notices) != 1 || notices[0].title != "5h额度已恢复" || !strings.Contains(notices[0].text, "nominal@example.test") {
		t.Fatalf("notices at the 5h reset = %+v", notices)
	}
	if notices := tracker.update(exhausted, primaryReset.Add(time.Minute)); len(notices) != 0 {
		t.Fatalf("the same recovery was announced twice: %+v", notices)
	}

	// After the forced refresh the 5h window is full again with a new reset time.
	refreshed := []accountQuota{{
		Email:     "nominal@example.test",
		Primary:   quotaBucket{Known: true, Percent: 100, ResetAt: primaryReset.Add(5 * time.Hour)},
		Secondary: exhausted[0].Secondary,
	}}
	if notices := tracker.update(refreshed, primaryReset.Add(time.Minute)); len(notices) != 0 {
		t.Fatalf("unexpected notices after refresh: %+v", notices)
	}
	notices = tracker.update(refreshed, weeklyReset)
	if len(notices) != 1 || notices[0].title != "周额度已恢复" {
		t.Fatalf("notices at the weekly reset = %+v", notices)
	}
}

func TestQuotaRecoveryTrackerCombinesWindowsAndSkipsEarlyRecovery(t *testing.T) {
	now := time.Date(2026, 10, 15, 10, 0, 0, 0, time.UTC)
	reset := now.Add(time.Hour)
	both := []accountQuota{{
		Email:     "nominal@example.test",
		Primary:   quotaBucket{Known: true, Percent: 0, ResetAt: reset},
		Secondary: quotaBucket{Known: true, Percent: 0, ResetAt: reset},
	}}
	tracker := newQuotaRecoveryTracker()
	tracker.update(both, now)
	if notices := tracker.update(both, reset); len(notices) != 1 || notices[0].title != "5h/周额度已恢复" {
		t.Fatalf("combined notices = %+v", notices)
	}

	// A reset credit restores the quota before the scheduled reset time: the
	// window was not "recovered by reset time", so nothing is announced.
	tracker = newQuotaRecoveryTracker()
	tracker.update(both[:1], now)
	restored := []accountQuota{{
		Email:     "nominal@example.test",
		Primary:   quotaBucket{Known: true, Percent: 100, ResetAt: now.Add(5 * time.Hour)},
		Secondary: quotaBucket{Known: true, Percent: 100, ResetAt: now.Add(7 * 24 * time.Hour)},
	}}
	tracker.update(restored, now.Add(time.Minute))
	if notices := tracker.update(restored, reset); len(notices) != 0 {
		t.Fatalf("early recovery should not be announced: %+v", notices)
	}

	// Windows that were never exhausted are never announced.
	tracker = newQuotaRecoveryTracker()
	partial := []accountQuota{{Email: "nominal@example.test", Primary: quotaBucket{Known: true, Percent: 3, ResetAt: reset}}}
	tracker.update(partial, now)
	if notices := tracker.update(partial, reset); len(notices) != 0 {
		t.Fatalf("a window above 0%% was announced: %+v", notices)
	}
}

func TestResetCreditLinesShowExpiryAndNoExpiryCount(t *testing.T) {
	now := time.Date(2026, 9, 23, 10, 0, 0, 0, time.UTC)
	info := resetCreditInfo{
		AvailableCount:         2,
		ExpiresAt:              []string{"2026-09-23T10:35:00Z"},
		WithoutExpiry:          1,
		ExpiryDetailsAvailable: true,
		ExpiryDetailsComplete:  true,
	}
	lines := resetCreditLines(info, now)
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "剩余 35分") || !strings.Contains(joined, "无固定到期时间：1 次") {
		t.Fatalf("reset details = %q", joined)
	}
}

func TestResetResultLocalizesAccountMessage(t *testing.T) {
	result := formatQuotaResetResponse(quotaResetResponse{
		Success: true,
		NominalAccounts: map[string]quotaResetAccountResult{
			"nominal@example.test": {Success: true, Message: "Codex quota reset credit consumed"},
		},
	})
	if !strings.Contains(result, "nominal@example.test：成功") || !strings.Contains(result, "已消耗 1 次重置额度") {
		t.Fatalf("reset result = %q", result)
	}
}

func TestQuotaColorUsesRedBlueGreenStops(t *testing.T) {
	if got := quotaColor(0); got != colorRef(232, 68, 68) {
		t.Fatalf("0%% color = %#x", got)
	}
	if got := quotaColor(50); got != colorRef(70, 145, 255) {
		t.Fatalf("50%% color = %#x", got)
	}
	if got := quotaColor(100); got != colorRef(52, 190, 116) {
		t.Fatalf("100%% color = %#x", got)
	}
}
