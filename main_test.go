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
	"path/filepath"
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

func TestFetchQuotaRetriesFiveTimes(t *testing.T) {
	attempts := 0
	var delays []time.Duration
	want := quotaSnapshot{Nominal: accountQuota{Email: "nominal@example.test"}}
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
	if got.Nominal.Email != want.Nominal.Email {
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
			if got.Nominal.Email != tt.wantEmail || got.DirectMode != tt.wantDirect {
				t.Fatalf("routed quota result did not match expected account or mode")
			}
			if got.ResetSupported == tt.wantDirect {
				t.Fatalf("ResetSupported = %v for direct mode %v", got.ResetSupported, tt.wantDirect)
			}
			if tt.wantDirect && (got.Nominal.Primary.Percent != 65 || got.Nominal.Reset.AvailableCount != 2 || got.ActualAvailable || !got.SingleAccount || !got.NominalAvailable) {
				t.Fatal("direct response quota or reset-credit data was not parsed correctly")
			}
		})
	}
}

func TestTrayAccountUsesDirectChatGPTQuota(t *testing.T) {
	chatGPTAccount := accountQuota{Email: "chatgpt@example.test", Primary: quotaBucket{Percent: 62, Known: true}}
	got := trayAccount(quotaSnapshot{Nominal: chatGPTAccount, NominalAvailable: true, SingleAccount: true, DirectMode: true})
	if got.Email != chatGPTAccount.Email || got.Primary.Percent != chatGPTAccount.Primary.Percent {
		t.Fatal("tray icon did not use the direct ChatGPT account quota")
	}

	got = trayAccount(quotaSnapshot{Nominal: chatGPTAccount, NominalAvailable: true, SingleAccount: true})
	if got.Email != chatGPTAccount.Email || got.Primary.Percent != chatGPTAccount.Primary.Percent {
		t.Fatal("tray icon did not use the only nominal account when actual_accounts is absent")
	}

	actualAccount := accountQuota{Email: "actual@example.test", Primary: quotaBucket{Percent: 37, Known: true}}
	got = trayAccount(quotaSnapshot{Nominal: chatGPTAccount, Actual: actualAccount, NominalAvailable: true, ActualAvailable: true})
	if got.Email != actualAccount.Email || got.Primary.Percent != actualAccount.Primary.Percent {
		t.Fatal("tray icon did not prefer actual-account quota in API-key mode")
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
	if !got.SingleAccount || !got.NominalAvailable || got.ActualAvailable {
		t.Fatal("single nominal account was not identified")
	}

	var multiple responseEnvelope
	if err := json.Unmarshal([]byte(`{"nominal_accounts":{"nominal@example.test":{"groups":[]}},"actual_accounts":{"actual@example.test":{"groups":[]}}}`), &multiple); err != nil {
		t.Fatal(err)
	}
	got = parseQuotaEnvelope(multiple)
	if got.SingleAccount || !got.ActualAvailable {
		t.Fatal("nominal-plus-actual response was incorrectly identified as a single account")
	}
}

func TestAuthBackupSelectsQuotaRouteWithoutExposingCredentials(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("user home directory is unavailable")
	}
	credentials, err := readQuotaCredentialsFromPath(filepath.Join(home, ".codex", "auth.bak"))
	if errors.Is(err, os.ErrNotExist) {
		t.Skip("local auth.bak fixture is not present")
	}
	if err != nil {
		t.Fatalf("could not parse local auth.bak fixture: %v", err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if credentials.Mode == authModeChatGPT {
			if r.URL.Path != "/direct" || r.Header.Get("Authorization") != "Bearer "+credentials.AccessToken || r.Header.Get("ChatGPT-Account-ID") != credentials.AccountID {
				t.Error("auth.bak credentials were not routed to the direct endpoint correctly")
			}
			_, _ = w.Write([]byte(`{"accounts":{"fixture@example.test":{"groups":[]}}}`))
			return
		}
		if r.URL.Path != "/apikey" || r.Header.Get("Authorization") != "Bearer "+credentials.APIKey {
			t.Error("auth.bak API key was not routed to the original endpoint correctly")
		}
		_, _ = w.Write([]byte(`{"nominal_accounts":{"fixture@example.test":{"groups":[]}}}`))
	}))
	defer server.Close()

	got, err := fetchQuotaUsingAuth(server.Client(), credentials, server.URL+"/apikey", server.URL+"/direct")
	if err != nil {
		t.Fatalf("auth.bak route smoke test failed: %v", err)
	}
	if got.DirectMode != (credentials.Mode == authModeChatGPT) || got.Nominal.Email != "fixture@example.test" {
		t.Fatal("auth.bak fixture selected an unexpected quota route")
	}
}

func TestParseAccountReadsResetCreditDetails(t *testing.T) {
	raw := json.RawMessage(`{"nominal@example.test":{"groups":[{"displayName":"Codex","buckets":[{"window":"primary","remainingFraction":0.75,"resetTime":"2026-09-24T15:00:00Z"},{"window":"secondary","remainingFraction":0.5,"reset_time":"2026-09-29T10:00:00Z"}]}],"reset_credits":{"available_count":2,"expires_at":["2026-09-24T12:00:00Z"],"without_expiry":1,"expiry_details_available":true,"expiry_details_complete":false}}}`)
	account := parseAccount(raw)
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
			if got.ActualAvailable != tt.available {
				t.Fatalf("ActualAvailable = %v, want %v", got.ActualAvailable, tt.available)
			}
		})
	}
}

func TestPanelHeightOmitsActualCardWhenFieldIsMissing(t *testing.T) {
	withoutActual := panelHeightFor(quotaSnapshot{})
	withActual := panelHeightFor(quotaSnapshot{ActualAvailable: true})
	if difference := withActual - withoutActual; difference != 10+actualCardHeight {
		t.Fatalf("actual card adds %d px, want %d px", difference, 10+actualCardHeight)
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
	if !strings.Contains(joined, "剩余 35 分钟") || !strings.Contains(joined, "无固定到期时间：1 次") {
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
