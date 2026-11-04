//go:build windows

package main

import (
	"encoding/json"
	"testing"
)

const kiroSample = `{"nominal_accounts":{"Kiro Student · Builder ID · 2438c458":{"subscription":{"plan":"KIRO STUDENT"},"groups":[{"displayName":"Kiro","buckets":[{"window":"monthly","remainingFraction":0.94354,"resetTime":"2026-12-01T00:00:00Z"}]}]},"Kiro Pro · api_key · e4b86438":{"subscription":{"plan":"KIRO PRO"},"groups":[{"displayName":"Kiro","buckets":[{"window":"monthly","remainingFraction":0.4664717,"resetTime":"2026-12-01T00:00:00Z"}]}]}},"partial":false}`

func TestParseKiroQuotaOneMonthlyGroupPerAccount(t *testing.T) {
	var envelope responseEnvelope
	if err := json.Unmarshal([]byte(kiroSample), &envelope); err != nil {
		t.Fatal(err)
	}
	quota, err := parseKiroQuota(envelope)
	if err != nil {
		t.Fatal(err)
	}
	if len(quota.Groups) != 2 || quota.Groups[0].Name != "Kiro Pro · api_key · e4b86438" {
		t.Fatalf("groups = %+v", quota.Groups)
	}
	pro := quota.Groups[0]
	if !pro.monthlyOnly() || pro.Monthly.Percent != 47 || pro.Monthly.ResetAt.IsZero() || pro.rowCount() != 1 {
		t.Errorf("pro group = %+v", pro)
	}
	if quota.Groups[1].Monthly.Percent != 94 {
		t.Errorf("student percent = %d", quota.Groups[1].Monthly.Percent)
	}
}

func TestParseKiroQuotaRejectsEmpty(t *testing.T) {
	if _, err := parseKiroQuota(responseEnvelope{}); err == nil {
		t.Error("empty envelope should fail")
	}
}

func TestExtQuotaURLOmitsEmptyEmail(t *testing.T) {
	got, err := extQuotaURL("https://kot.xkcht.eu.org", providerKiro, "")
	if err != nil || got[len(got)-len("?provider=kiro"):] != "?provider=kiro" {
		t.Errorf("url = %q, err = %v", got, err)
	}
}
