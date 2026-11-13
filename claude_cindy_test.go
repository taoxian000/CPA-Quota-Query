//go:build windows

package main

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const claudeCindySample = `{"available":true,"subscriptionType":"pro","fiveHour":{"usedPercent":17,"remainingPercent":83,"resetsAt":"2026-11-13T18:10:00.080Z"},"sevenDay":{"usedPercent":7,"remainingPercent":93,"resetsAt":"2026-11-14T05:00:00.080Z"},"scoped":[{"model":"Fable","modelId":null,"usedPercent":13,"remainingPercent":87,"resetsAt":"2026-11-14T05:00:00.080Z"}],"observedAt":"2026-11-13T14:19:10.031Z"}`

func TestParseClaudeCindyQuota(t *testing.T) {
	quota, err := parseClaudeCindyQuota(strings.NewReader(claudeCindySample), "display@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if !quota.connected() || quota.Email != "display@example.com" || quota.Plan != "pro" || len(quota.Groups) != 2 {
		t.Fatalf("unexpected quota: %+v", quota)
	}
	all, scoped := quota.Groups[0], quota.Groups[1]
	if !all.Primary.Known || all.Primary.Percent != 83 || !all.Secondary.Known || all.Secondary.Percent != 93 {
		t.Fatalf("overall remaining quota = %+v, want 83/93", all)
	}
	if scoped.Name != "Fable" || !scoped.weeklyOnly() || scoped.Secondary.Percent != 87 || scoped.rowCount() != 1 {
		t.Fatalf("scoped group should have one weekly meter: %+v", scoped)
	}
	if got, want := all.Primary.ResetAt, time.Date(2026, 11, 13, 18, 10, 0, 80000000, time.UTC); !got.Equal(want) {
		t.Fatalf("5h reset time = %v, want %v", got, want)
	}
	if got, want := all.Secondary.ResetAt, time.Date(2026, 11, 14, 5, 0, 0, 80000000, time.UTC); !got.Equal(want) || !scoped.Secondary.ResetAt.Equal(want) {
		t.Fatalf("weekly reset times = %v/%v, want %v", got, scoped.Secondary.ResetAt, want)
	}
	if got := extCardHeight(quota); got != 174 {
		t.Fatalf("card height = %d, want 174 for 3 meters and 2 group headers", got)
	}
	state := appState{settings: appSettings{}.withProvider(providerClaudeCindy, providerConfig{Enabled: true}), ext: map[string]extQuota{providerClaudeCindy: quota}}
	var resetTimes []time.Time
	for _, at := range extResetTimes(&state) {
		if !at.IsZero() {
			resetTimes = append(resetTimes, at)
		}
	}
	if len(resetTimes) != 3 {
		t.Fatalf("expiry loop got %d reset times, want all 3 windows", len(resetTimes))
	}
}

func TestParseClaudeCindyQuotaOptionalData(t *testing.T) {
	body := `{"available":true,"fiveHour":{"usedPercent":17.4,"remainingPercent":0},"sevenDay":{"usedPercent":6.6,"resetsAt":null},"scoped":[{"model":"missing"},{"modelId":"model-id","remainingPercent":105},{"remainingPercent":-5}]}`
	quota, err := parseClaudeCindyQuota(strings.NewReader(body), "local label")
	if err != nil {
		t.Fatal(err)
	}
	if len(quota.Groups) != 3 {
		t.Fatalf("groups = %+v, want overall plus 2 known scoped windows", quota.Groups)
	}
	if quota.Groups[0].Primary.Percent != 0 || quota.Groups[0].Secondary.Percent != 93 || !quota.Groups[0].Secondary.ResetAt.IsZero() {
		t.Fatalf("zero remaining, used fallback or null reset handled incorrectly: %+v", quota.Groups[0])
	}
	if quota.Groups[1].Name != "model-id" || quota.Groups[1].Secondary.Percent != 100 || quota.Groups[2].Secondary.Percent != 0 {
		t.Fatalf("scoped fallback names or clamping handled incorrectly: %+v", quota.Groups[1:])
	}
	for _, scoped := range []string{"null", "[]"} {
		quota, err := parseClaudeCindyQuota(strings.NewReader(`{"available":true,"fiveHour":{"remainingPercent":83},"scoped":`+scoped+`}`), "")
		if err != nil || len(quota.Groups) != 1 {
			t.Fatalf("scoped %s: quota=%+v, err=%v", scoped, quota, err)
		}
	}
}

func TestParseClaudeCindyQuotaRejectsUnavailableOrMissingData(t *testing.T) {
	for _, body := range []string{`{"available":false}`, `{}`, `{"available":true}`, `{"available":true,"fiveHour":{"remainingPercent":null}}`, `null`, `invalid json`} {
		if _, err := parseClaudeCindyQuota(strings.NewReader(body), ""); err == nil {
			t.Errorf("expected error for %s", body)
		}
	}
}

func TestValidateClaudeCindyInput(t *testing.T) {
	for _, endpoint := range []string{defaultClaudeCindyURL, "https://example.com/custom/usage/", "http://localhost:8765/usage"} {
		cfg, err := validateProviderInput(providerClaudeCindy, "  "+endpoint+"  ", "  label@example.com  ")
		if err != nil || !cfg.Enabled || cfg.BaseURL != endpoint || cfg.Email != "label@example.com" {
			t.Fatalf("endpoint %q: cfg=%+v, err=%v", endpoint, cfg, err)
		}
		settings := appSettings{}.withProvider(providerClaudeCindy, cfg)
		encoded, err := json.Marshal(settings)
		if err != nil {
			t.Fatal(err)
		}
		var restored appSettings
		if err := json.Unmarshal(encoded, &restored); err != nil {
			t.Fatal(err)
		}
		if got := restored.providerConfigFor(providerClaudeCindy); got != cfg {
			t.Fatalf("saved config = %+v, want %+v", got, cfg)
		}
	}
	for _, endpoint := range []string{"", "example.com/usage", "ftp://example.com/usage", "http://user:secret@example.com/usage", "http://example.com/usage?email=x", "http://example.com/usage?", "http://example.com/usage#part", "http://example.com/usage#", "http:///usage"} {
		if _, err := validateProviderInput(providerClaudeCindy, endpoint, "label@example.com"); err == nil {
			t.Errorf("expected invalid URL: %q", endpoint)
		}
	}
	if _, err := validateProviderInput(providerClaudeCindy, defaultClaudeCindyURL, "  "); err == nil {
		t.Error("expected error for empty display email")
	}
	if _, err := validateProviderInput(providerClaude, defaultClaudeCindyURL, "label@example.com"); err == nil {
		t.Error("existing authenticated Claude provider must still reject LAN HTTP")
	}
}

func TestFetchClaudeCindyQuotaUsesExactURLWithoutCredentials(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.RequestURI != "/custom/usage/" {
			t.Errorf("unexpected request: %s %s", r.Method, r.RequestURI)
		}
		for _, header := range []string{"Authorization", "Proxy-Authorization", "Cookie"} {
			if r.Header.Get(header) != "" {
				t.Errorf("unexpected %s header", header)
			}
		}
		body, err := io.ReadAll(r.Body)
		if err != nil || len(body) != 0 {
			t.Errorf("unexpected request body: %q, err=%v", body, err)
		}
		io.WriteString(w, claudeCindySample)
	}))
	defer server.Close()
	t.Setenv("USERPROFILE", t.TempDir())
	quota, err := fetchExtQuota(providerClaudeCindy, providerConfig{BaseURL: server.URL + "/custom/usage/", Email: "display-only@example.com", APIKey: "must-not-be-sent"})
	if err != nil {
		t.Fatal(err)
	}
	if quota.Email != "display-only@example.com" || len(quota.Groups) != 2 {
		t.Fatalf("unexpected quota: %+v", quota)
	}
}

func TestFetchClaudeCindyQuotaHTTPFailuresAndRedirects(t *testing.T) {
	for _, status := range []int{http.StatusForbidden, http.StatusServiceUnavailable, http.StatusFound} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			followed := false
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/redirected" {
					followed = true
					io.WriteString(w, claudeCindySample)
					return
				}
				w.Header().Set("Location", "/redirected")
				w.WriteHeader(status)
			}))
			defer server.Close()
			_, err := fetchClaudeCindyQuotaAt(newQuotaClient(nil), server.URL+"/usage", "")
			var statusErr *httpStatusError
			if !errors.As(err, &statusErr) || statusErr.code != status || followed {
				t.Fatalf("err=%v, followed=%v, want HTTP %d without redirect", err, followed, status)
			}
		})
	}
}
