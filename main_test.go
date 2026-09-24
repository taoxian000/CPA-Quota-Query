//go:build windows

package main

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

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
		{name: "hours", at: now.Add(4*time.Hour + 45*time.Minute), want: "剩余时间：4小时"},
		{name: "days and hours", at: now.Add(6*24*time.Hour + 3*time.Hour + 20*time.Minute), want: "剩余时间：6天3小时"},
		{name: "minutes under an hour", at: now.Add(42 * time.Minute), want: "剩余时间：42分钟"},
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
