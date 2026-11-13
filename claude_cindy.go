//go:build windows

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const defaultClaudeCindyURL = "http://192.168.160.20:8765/usage"

type claudeCindyWindow struct {
	UsedPercent      *float64 `json:"usedPercent"`
	RemainingPercent *float64 `json:"remainingPercent"`
	ResetsAt         string   `json:"resetsAt"`
}

type claudeCindyResponse struct {
	Available        bool              `json:"available"`
	SubscriptionType string            `json:"subscriptionType"`
	FiveHour         claudeCindyWindow `json:"fiveHour"`
	SevenDay         claudeCindyWindow `json:"sevenDay"`
	Scoped           []struct {
		Model   string `json:"model"`
		ModelID string `json:"modelId"`
		claudeCindyWindow
	} `json:"scoped"`
}

func (window claudeCindyWindow) bucket() quotaBucket {
	var remaining float64
	switch {
	case window.RemainingPercent != nil:
		remaining = *window.RemainingPercent
	case window.UsedPercent != nil:
		remaining = 100 - *window.UsedPercent
	default:
		return quotaBucket{}
	}
	return quotaBucket{
		Percent: max(0, min(int(math.Round(remaining)), 100)),
		Known:   true,
		ResetAt: parseQuotaResetTime(window.ResetsAt),
	}
}

// This endpoint is unauthenticated, so HTTP is supported for LAN services.
// Keep the complete path intact and reject query parameters and user info.
func normalizeClaudeCindyURL(input string) (string, error) {
	value := strings.TrimSpace(input)
	if value == "" {
		return "", errors.New("请输入 URL。")
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Hostname() == "" || parsed.User != nil ||
		(parsed.Scheme != "http" && parsed.Scheme != "https") ||
		parsed.RawQuery != "" || parsed.ForceQuery || strings.Contains(value, "#") {
		return "", errors.New("URL 无效：请输入完整的 HTTP 或 HTTPS 额度接口地址，不能包含用户名、密码、查询参数或片段。")
	}
	return parsed.String(), nil
}

func validateClaudeCindyInput(endpoint, email string) (providerConfig, error) {
	endpoint, err := normalizeClaudeCindyURL(endpoint)
	if err != nil {
		return providerConfig{}, err
	}
	email = strings.TrimSpace(email)
	if email == "" {
		return providerConfig{}, errors.New("请输入 email（仅用于显示）。")
	}
	return providerConfig{Enabled: true, BaseURL: endpoint, Email: email}, nil
}

func parseClaudeCindyQuota(body io.Reader, email string) (extQuota, error) {
	var response claudeCindyResponse
	if err := json.NewDecoder(io.LimitReader(body, maxQuotaResponseBytes)).Decode(&response); err != nil {
		return extQuota{}, fmt.Errorf("解析额度响应失败：%w", err)
	}
	if !response.Available {
		return extQuota{}, errors.New("Claude(Cindy) 额度数据暂不可用")
	}
	group := extGroup{Name: "全部模型", Primary: response.FiveHour.bucket(), Secondary: response.SevenDay.bucket()}
	if !group.Primary.Known && !group.Secondary.Known {
		return extQuota{}, errors.New("响应中没有 5 小时或 7 天额度数据")
	}
	result := extQuota{Fetched: true, Email: email, Plan: response.SubscriptionType, Groups: []extGroup{group}}
	for _, scoped := range response.Scoped {
		bucket := scoped.bucket()
		if !bucket.Known {
			continue
		}
		name := strings.TrimSpace(scoped.Model)
		if name == "" {
			name = strings.TrimSpace(scoped.ModelID)
		}
		if name == "" {
			name = "模型额度"
		}
		result.Groups = append(result.Groups, extGroup{Name: name, Secondary: bucket})
	}
	return result, nil
}

func fetchClaudeCindyQuotaAt(client *http.Client, endpoint, email string) (extQuota, error) {
	req, err := http.NewRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		return extQuota{}, err
	}
	req.Header.Set("User-Agent", userAgent())
	resp, err := client.Do(req)
	if err != nil {
		return extQuota{}, fmt.Errorf("额度接口请求失败：%w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return extQuota{}, &httpStatusError{operation: "额度接口", code: resp.StatusCode}
	}
	return parseClaudeCindyQuota(resp.Body, email)
}

func fetchClaudeCindyQuota(cfg providerConfig) (extQuota, error) {
	endpoint, err := normalizeClaudeCindyURL(cfg.BaseURL)
	if err != nil {
		return extQuota{}, err
	}
	client := newQuotaClient(nil)
	var result extQuota
	_, err = fetchQuotaWithRetry(func() (quotaSnapshot, error) {
		var requestErr error
		result, requestErr = fetchClaudeCindyQuotaAt(client, endpoint, cfg.Email)
		return quotaSnapshot{}, requestErr
	}, time.Sleep)
	return result, err
}
