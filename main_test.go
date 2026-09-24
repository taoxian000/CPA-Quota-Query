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
	valid := githubRelease{
		TagName: "v1.2",
		Assets: []releaseAsset{{
			Name:               updateAssetName,
			BrowserDownloadURL: "https://github.com/taoxian000/CPA-Quota-Query/releases/download/v1.2/" + updateAssetName,
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

	tests := []struct {
		name    string
		release githubRelease
	}{
		{name: "missing exe", release: githubRelease{TagName: "v1.2"}},
		{name: "prerelease", release: githubRelease{TagName: "v1.2", Prerelease: true, Assets: valid.Assets}},
		{name: "invalid tag", release: githubRelease{TagName: "v1.2-rc1", Assets: valid.Assets}},
		{name: "invalid download host", release: githubRelease{TagName: "v1.2", Assets: []releaseAsset{{Name: updateAssetName, BrowserDownloadURL: "https://example.com/app.exe", Digest: valid.Assets[0].Digest, Size: valid.Assets[0].Size}}}},
		{name: "missing digest", release: githubRelease{TagName: "v1.2", Assets: []releaseAsset{{Name: updateAssetName, BrowserDownloadURL: valid.Assets[0].BrowserDownloadURL, Size: valid.Assets[0].Size}}}},
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
	encoded, err := json.Marshal(githubRelease{
		TagName: "v1.2",
		Assets: []releaseAsset{{
			Name:               updateAssetName,
			BrowserDownloadURL: "https://github.com/taoxian000/CPA-Quota-Query/releases/download/v1.2/" + updateAssetName,
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
