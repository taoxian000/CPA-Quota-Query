//go:build windows

package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const factoryLimitsSample = `{"usesTokenRateLimitsBilling":true,"limits":{"standard":{"fiveHour":{"usedPercent":8,"windowEnd":"2026-10-29T10:33:14.928Z","secondsRemaining":13074},"weekly":{"usedPercent":3,"windowEnd":"2026-11-05T05:33:14.928Z","secondsRemaining":599874},"monthly":{"usedPercent":2,"windowEnd":"2026-11-28T05:33:14.928Z","secondsRemaining":2587074}},"core":{"fiveHour":{"usedPercent":0,"windowEnd":null,"secondsRemaining":null},"weekly":{"usedPercent":0,"windowEnd":null,"secondsRemaining":null},"monthly":{"usedPercent":0,"windowEnd":null,"secondsRemaining":null}}},"overagePreference":"droidCore","canManageOverage":true,"extraUsageBalanceCents":0,"extraUsageAllowed":true}`

func TestParseFactoryLimitsShowsStandardOnly(t *testing.T) {
	quota, err := parseFactoryLimits(strings.NewReader(factoryLimitsSample))
	if err != nil {
		t.Fatal(err)
	}
	if !quota.Fetched || len(quota.Groups) != 1 {
		t.Fatalf("want one fetched group, got %+v", quota)
	}
	group := quota.Groups[0]
	if group.Primary.Percent != 92 || group.Secondary.Percent != 97 || group.Monthly.Percent != 98 {
		t.Fatalf("remaining percents = %d/%d/%d, want 92/97/98", group.Primary.Percent, group.Secondary.Percent, group.Monthly.Percent)
	}
	if group.Primary.ResetAt.IsZero() || group.Monthly.ResetAt.IsZero() {
		t.Fatal("reset times were not parsed")
	}
	if group.rowCount() != 3 {
		t.Fatalf("rowCount = %d, want 3", group.rowCount())
	}
}

func TestParseFactoryLimitsAddsCoreWhenStandardExhausted(t *testing.T) {
	body := strings.Replace(factoryLimitsSample, `"usedPercent":8`, `"usedPercent":100`, 1)
	quota, err := parseFactoryLimits(strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if len(quota.Groups) != 2 || quota.Groups[1].Name != "Core" {
		t.Fatalf("want Standard then Core, got %+v", quota.Groups)
	}
	if quota.Groups[0].Primary.Percent != 0 {
		t.Fatalf("exhausted window remaining = %d, want 0", quota.Groups[0].Primary.Percent)
	}
	core := quota.Groups[1].Primary
	if !core.Known || core.Percent != 100 || !core.ResetAt.IsZero() {
		t.Fatalf("idle core window = %+v, want known 100%% with no reset time", core)
	}
}

func TestParseFactoryLimitsRejectsMissingStandard(t *testing.T) {
	if _, err := parseFactoryLimits(strings.NewReader(`{"limits":{}}`)); err == nil {
		t.Fatal("expected an error when standard limits are missing")
	}
}

func TestFetchFactoryQuotaSendsExpectedHeaders(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for name, want := range map[string]string{
			"Authorization": "Bearer secret-key",
			"Content-Type":  "application/json",
			"Origin":        "https://app.factory.ai",
			"Referer":       "https://app.factory.ai/",
		} {
			if got := r.Header.Get(name); got != want {
				t.Errorf("%s = %q, want %q", name, got, want)
			}
		}
		w.Write([]byte(factoryLimitsSample))
	}))
	defer server.Close()

	quota, err := fetchFactoryQuotaAt(server.Client(), server.URL+"/api/billing/limits", "secret-key")
	if err != nil {
		t.Fatal(err)
	}
	if len(quota.Groups) != 1 {
		t.Fatalf("groups = %d, want 1", len(quota.Groups))
	}
}

func TestNormalizeFactoryKey(t *testing.T) {
	if key, err := normalizeFactoryKey("  fk-123  "); err != nil || key != "fk-123" {
		t.Fatalf("got %q, %v", key, err)
	}
	for _, bad := range []string{"", "   ", "fk 123", "Bearer fk-123"} {
		if _, err := normalizeFactoryKey(bad); err == nil {
			t.Errorf("normalizeFactoryKey(%q) should fail", bad)
		}
	}
}
