//go:build windows

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
	"time"
	"unicode"
)

const factoryLimitsURL = "https://api.factory.ai/api/billing/limits"

type factoryWindow struct {
	UsedPercent *float64 `json:"usedPercent"`
	WindowEnd   *string  `json:"windowEnd"`
}

type factoryTier struct {
	FiveHour factoryWindow `json:"fiveHour"`
	Weekly   factoryWindow `json:"weekly"`
	Monthly  factoryWindow `json:"monthly"`
}

type factoryLimitsResponse struct {
	Limits struct {
		Standard *factoryTier `json:"standard"`
		Core     *factoryTier `json:"core"`
	} `json:"limits"`
}

// bucket converts usedPercent into the remaining percentage the panel shows.
// A null windowEnd (an idle window) leaves ResetAt zero.
func (window factoryWindow) bucket() quotaBucket {
	if window.UsedPercent == nil {
		return quotaBucket{}
	}
	percent := 100 - int(math.Round(*window.UsedPercent))
	bucket := quotaBucket{Percent: max(0, min(percent, 100)), Known: true}
	if window.WindowEnd != nil {
		bucket.ResetAt = parseQuotaResetTime(*window.WindowEnd)
	}
	return bucket
}

func (tier factoryTier) group(name string) extGroup {
	return extGroup{Name: name, Primary: tier.FiveHour.bucket(), Secondary: tier.Weekly.bucket(), Monthly: tier.Monthly.bucket()}
}

func (tier factoryTier) exhausted() bool {
	for _, window := range []factoryWindow{tier.FiveHour, tier.Weekly, tier.Monthly} {
		if window.UsedPercent != nil && *window.UsedPercent >= 100 {
			return true
		}
	}
	return false
}

// rowCount is the number of meter rows a group draws; only Factory Droid has
// a monthly window.
func (group extGroup) rowCount() int {
	if group.Monthly.Known {
		return 3
	}
	return 2
}

// parseFactoryLimits shows the standard tier, and adds the core tier only once
// a standard window has run out (core is the overage pool).
func parseFactoryLimits(body io.Reader) (extQuota, error) {
	var response factoryLimitsResponse
	if err := json.NewDecoder(io.LimitReader(body, maxQuotaResponseBytes)).Decode(&response); err != nil {
		return extQuota{}, fmt.Errorf("解析额度响应失败：%w", err)
	}
	standard := response.Limits.Standard
	if standard == nil {
		return extQuota{}, errors.New("响应中没有 standard 额度")
	}
	group := standard.group("Standard")
	if !group.Primary.Known && !group.Secondary.Known && !group.Monthly.Known {
		return extQuota{}, errors.New("响应中没有 standard 额度数据")
	}
	result := extQuota{Fetched: true, Groups: []extGroup{group}}
	if standard.exhausted() && response.Limits.Core != nil {
		result.Groups = append(result.Groups, response.Limits.Core.group("Core"))
	}
	return result, nil
}

func fetchFactoryQuotaAt(client *http.Client, endpoint, apiKey string) (extQuota, error) {
	req, err := http.NewRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		return extQuota{}, err
	}
	req.Header.Set("User-Agent", userAgent())
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "https://app.factory.ai")
	req.Header.Set("Referer", "https://app.factory.ai/")
	resp, err := client.Do(req)
	if err != nil {
		return extQuota{}, fmt.Errorf("额度接口请求失败：%w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return extQuota{}, &httpStatusError{operation: "额度接口", code: resp.StatusCode}
	}
	return parseFactoryLimits(resp.Body)
}

func fetchFactoryQuota(cfg providerConfig) (extQuota, error) {
	if cfg.APIKey == "" {
		return extQuota{}, errors.New("尚未设置 API Key")
	}
	client := newQuotaClient(nil)
	var result extQuota
	_, err := fetchQuotaWithRetry(func() (quotaSnapshot, error) {
		var requestErr error
		result, requestErr = fetchFactoryQuotaAt(client, factoryLimitsURL, cfg.APIKey)
		return quotaSnapshot{}, requestErr
	}, time.Sleep)
	return result, err
}

func normalizeFactoryKey(input string) (string, error) {
	key := strings.TrimSpace(input)
	if key == "" {
		return "", errors.New("请输入 API Key。")
	}
	if strings.IndexFunc(key, unicode.IsSpace) >= 0 || strings.IndexFunc(key, unicode.IsControl) >= 0 {
		return "", errors.New("API Key 不能包含空白字符。")
	}
	return key, nil
}

// finishFactoryDialog reads the key typed into the dialog's first edit box.
// It returns false (keeping the dialog open) when "启用" is pressed with an
// unusable key.
func (a *monitorApp) finishFactoryDialog(dlg *providerDialog, command uint16) bool {
	key := dlg.baseURL
	dlg.baseURL, dlg.email = "", ""
	if command == idDialogOK {
		normalized, err := normalizeFactoryKey(key)
		if err != nil {
			a.messageBox(dlg.hwnd, err.Error(), mbOK|mbIconWarning)
			return false
		}
		dlg.apiKey, dlg.result = normalized, dlgEnable
		return true
	}
	dlg.result = dlgDisable
	dlg.apiKey = dlg.current.APIKey
	if normalized, err := normalizeFactoryKey(key); err == nil {
		dlg.apiKey = normalized
	}
	return true
}
