//go:build windows

package main

import (
	"encoding/json"
	"errors"
	"sort"
)

// parseKiroQuota reads every account of the Kiro route's nominal_accounts
// envelope. Each account is one group holding a single monthly credit bucket.
func parseKiroQuota(envelope responseEnvelope) (extQuota, error) {
	var accounts map[string]extAccountDetails
	for _, raw := range []json.RawMessage{envelope.Nominal, envelope.Actual} {
		var candidate map[string]extAccountDetails
		if len(raw) > 0 && json.Unmarshal(raw, &candidate) == nil && len(candidate) > 0 {
			accounts = candidate
			break
		}
	}
	if len(accounts) == 0 {
		return extQuota{}, errors.New("响应中没有账户数据")
	}
	names := make([]string, 0, len(accounts))
	for name := range accounts {
		names = append(names, name)
	}
	sort.Strings(names)

	result := extQuota{Fetched: true}
	for _, name := range names {
		group := extGroup{Name: name}
		known := false
		for _, item := range accounts[name].Groups {
			for _, raw := range item.Buckets {
				bucket, ok := quotaBucketFromRaw(raw)
				if !ok {
					continue
				}
				switch raw.Window {
				case "primary":
					group.Primary, known = bucket, true
				case "secondary":
					group.Secondary, known = bucket, true
				default:
					group.Monthly, known = bucket, true
				}
			}
		}
		if known {
			result.Groups = append(result.Groups, group)
		}
	}
	if len(result.Groups) == 0 {
		return extQuota{}, errors.New("响应中没有 Kiro 额度数据")
	}
	if len(result.Groups) == 1 {
		result.Email = result.Groups[0].Name
	}
	return result, nil
}
