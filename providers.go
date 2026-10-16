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
	"syscall"
	"time"
	"unsafe"
)

// Providers are identified by their lowercase id everywhere data leaves the
// program (settings.json, the provider= query parameter); providerTitle only
// capitalises for the UI.
const (
	providerCodex       = "codex"
	providerAntigravity = "antigravity"
	providerClaude      = "claude"
)

var providerOrder = []string{providerCodex, providerAntigravity, providerClaude}

func providerTitle(id string) string {
	if id == "" {
		return id
	}
	return strings.ToUpper(id[:1]) + id[1:]
}

// providerConfig is one provider's saved dialog values. Codex keeps its base
// URL in appSettings.ServerURL (as before providers existed); Email is stored
// for it but the Codex request does not use it.
type providerConfig struct {
	Enabled bool
	BaseURL string `json:",omitempty"`
	Email   string `json:",omitempty"`
}

// providerEnabled treats Codex as enabled until the user says otherwise, so
// settings files written before providers existed keep working.
func (s appSettings) providerEnabled(id string) bool {
	if cfg, ok := s.Providers[id]; ok {
		return cfg.Enabled
	}
	return id == providerCodex
}

func (s appSettings) providerConfigFor(id string) providerConfig {
	cfg, ok := s.Providers[id]
	if !ok {
		cfg.Enabled = id == providerCodex
	}
	if id == providerCodex {
		cfg.BaseURL = s.ServerURL
	}
	return cfg
}

// withProvider returns a copy with cfg stored for id. The map is copied
// because snapshots share the old one with other goroutines.
func (s appSettings) withProvider(id string, cfg providerConfig) appSettings {
	next := make(map[string]providerConfig, len(s.Providers)+1)
	for key, value := range s.Providers {
		next[key] = value
	}
	if id == providerCodex {
		s.ServerURL = cfg.BaseURL
		cfg.BaseURL = ""
	}
	next[id] = cfg
	s.Providers = next
	return s
}

// normalizeBaseURL accepts "host/path" or a full URL and returns a URL with a
// scheme. Credentials are only sent over HTTPS (or HTTP to loopback), exactly
// like the Codex route.
func normalizeBaseURL(input string, allowEmpty bool) (string, error) {
	value := strings.TrimSpace(input)
	if value == "" {
		if allowEmpty {
			return "", nil
		}
		return "", errors.New("请输入 base_url。")
	}
	if !strings.Contains(value, "://") {
		value = "https://" + value
	}
	if _, err := quotaEndpointsFor(value); err != nil {
		return "", fmt.Errorf("base_url 无效：%q\n\n需要 HTTPS 地址（仅本机地址允许 HTTP），不能带查询参数。", input)
	}
	return strings.TrimRight(value, "/"), nil
}

func validateProviderInput(id, baseURL, email string) (providerConfig, error) {
	base, err := normalizeBaseURL(baseURL, id == providerCodex)
	if err != nil {
		return providerConfig{}, err
	}
	email = strings.TrimSpace(email)
	if id != providerCodex && email == "" {
		return providerConfig{}, errors.New("请输入 email。")
	}
	if id == providerCodex && base == defaultServerURL {
		base = ""
	}
	return providerConfig{Enabled: true, BaseURL: base, Email: email}, nil
}

// ---- Antigravity / Claude quota ------------------------------------------

type extGroup struct {
	Name      string
	Primary   quotaBucket
	Secondary quotaBucket
}

type extQuota struct {
	Fetched bool // at least one successful sync with the current settings
	Failed  bool // the latest attempt failed (Groups then hold older data)
	Err     string
	Email   string
	Plan    string
	Groups  []extGroup
}

func (q extQuota) connected() bool { return q.Fetched && !q.Failed }

type extAccountDetails struct {
	Subscription struct {
		Plan     string `json:"plan"`
		TierName string `json:"tierName"`
	} `json:"subscription"`
	Groups []quotaGroup `json:"groups"`
}

func quotaBucketFromRaw(raw quotaBucketRaw) (quotaBucket, bool) {
	if raw.RemainingFraction == nil {
		return quotaBucket{}, false
	}
	percent := int(math.Round(*raw.RemainingFraction * 100))
	percent = max(0, min(percent, 100))
	resetAt := raw.ResetTime
	if resetAt == "" {
		resetAt = raw.ResetTimeSnake
	}
	return quotaBucket{Percent: percent, Known: true, ResetAt: parseQuotaResetTime(resetAt)}, true
}

// parseExtQuota reads the {"nominal_accounts": {email: {...}}} envelope used
// by the user-routing route. Antigravity returns several groups (one per model
// family), Claude one; every group carries a primary (5h) and a secondary
// (weekly) bucket.
func parseExtQuota(envelope responseEnvelope, email string) (extQuota, error) {
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
	foundEmail := ""
	var details extAccountDetails
	for key, value := range accounts {
		if strings.EqualFold(key, email) {
			foundEmail, details = key, value
			break
		}
	}
	if foundEmail == "" {
		if len(accounts) != 1 {
			return extQuota{}, fmt.Errorf("响应中没有账户 %s", email)
		}
		for key, value := range accounts {
			foundEmail, details = key, value
		}
	}
	result := extQuota{Fetched: true, Email: foundEmail, Plan: details.Subscription.TierName}
	if result.Plan == "" {
		result.Plan = details.Subscription.Plan
	}
	for _, group := range details.Groups {
		item := extGroup{Name: group.DisplayName}
		any := false
		for _, raw := range group.Buckets {
			bucket, ok := quotaBucketFromRaw(raw)
			if !ok {
				continue
			}
			switch raw.Window {
			case "primary":
				item.Primary, any = bucket, true
			case "secondary":
				item.Secondary, any = bucket, true
			}
		}
		if any {
			result.Groups = append(result.Groups, item)
		}
	}
	return result, nil
}

func extQuotaURL(baseURL, provider, email string) (string, error) {
	endpoints, err := quotaEndpointsFor(baseURL)
	if err != nil {
		return "", err
	}
	query := url.Values{"provider": {provider}, "email": {email}}
	return endpoints.quota + "?" + query.Encode(), nil
}

// extAPIKey returns the same key Codex uses for the quota route.
func extAPIKey() (string, error) {
	credentials, err := readQuotaCredentials()
	if err != nil {
		return "", err
	}
	if credentials.Mode != authModeAPIKey {
		return "", errors.New("auth.json 不是 API Key 模式，没有可用于该接口的 Key")
	}
	return credentials.APIKey, nil
}

func fetchExtQuotaAt(client *http.Client, endpoint, apiKey, provider, email string) (extQuota, error) {
	req, err := http.NewRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		return extQuota{}, err
	}
	req.Header.Set("User-Agent", userAgent())
	req.Header.Set("Authorization", "Bearer "+apiKey)
	resp, err := client.Do(req)
	if err != nil {
		return extQuota{}, fmt.Errorf("额度接口请求失败：%w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return extQuota{}, &httpStatusError{operation: "额度接口", code: resp.StatusCode}
	}
	var envelope responseEnvelope
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxQuotaResponseBytes)).Decode(&envelope); err != nil {
		return extQuota{}, fmt.Errorf("解析额度响应失败：%w", err)
	}
	return parseExtQuota(envelope, email)
}

func fetchExtQuota(provider string, cfg providerConfig) (extQuota, error) {
	if strings.TrimSpace(cfg.Email) == "" {
		return extQuota{}, errors.New("尚未设置 email")
	}
	if strings.TrimSpace(cfg.BaseURL) == "" {
		return extQuota{}, errors.New("尚未设置 base_url")
	}
	endpoint, err := extQuotaURL(cfg.BaseURL, provider, cfg.Email)
	if err != nil {
		return extQuota{}, err
	}
	apiKey, err := extAPIKey()
	if err != nil {
		return extQuota{}, err
	}
	client := newQuotaClient(nil)
	var result extQuota
	_, err = fetchQuotaWithRetry(func() (quotaSnapshot, error) {
		var requestErr error
		result, requestErr = fetchExtQuotaAt(client, endpoint, apiKey, provider, cfg.Email)
		return quotaSnapshot{}, requestErr
	}, time.Sleep)
	return result, err
}

// extResetTimes lists every reset time of the enabled Antigravity/Claude
// accounts so the expiry loop refreshes when any of them comes due.
func extResetTimes(s *appState) []time.Time {
	var times []time.Time
	for _, id := range providerOrder {
		if id == providerCodex || !s.settings.providerEnabled(id) {
			continue
		}
		for _, group := range s.ext[id].Groups {
			times = append(times, group.Primary.ResetAt, group.Secondary.ResetAt)
		}
	}
	return times
}

// trayTarget picks what the tray icon and tooltip describe: the first enabled
// provider that has data.
func trayTarget(s *appState) (string, accountQuota) {
	for _, id := range providerOrder {
		if !s.settings.providerEnabled(id) {
			continue
		}
		if id == providerCodex {
			if account := trayAccount(s.quota); account.Primary.Known || account.Secondary.Known || account.Email != "" {
				return providerTitle(id), account
			}
			continue
		}
		q := s.ext[id]
		if q.Fetched && len(q.Groups) > 0 {
			return providerTitle(id), accountQuota{Email: q.Email, Primary: q.Groups[0].Primary, Secondary: q.Groups[0].Secondary}
		}
	}
	return providerTitle(providerCodex), accountQuota{}
}

// ---- panel sections --------------------------------------------------------

const (
	bandHeight           = 30
	extGroupHeaderHeight = 20
	extRowPitch          = 24
	extGroupGap          = 10 // extra space between two Antigravity quota groups
	extDisconnectedBody  = 72
	emptyPanelHintHeight = 56
)

type panelSection struct {
	provider  string
	top       int32
	height    int32
	connected bool
	cards     []panelCard // Codex: one card per account
	ext       extQuota    // Antigravity / Claude
	email     string      // configured email, shown before the first sync
}

func extCardHeight(q extQuota) int32 {
	if !q.Fetched {
		return extDisconnectedBody
	}
	height := int32(bandHeight + 10 + 12)
	for range max(len(q.Groups), 1) {
		if len(q.Groups) > 1 {
			height += extGroupHeaderHeight + extGroupGap
		}
		height += 2 * extRowPitch
	}
	if len(q.Groups) > 1 {
		height -= extGroupGap // no gap after the last group
	}
	return height
}

// sectionLayout stacks one section per enabled provider, in providerOrder.
func sectionLayout(s *appState) ([]panelSection, int32) {
	top := int32(panelHeaderHeight)
	var sections []panelSection
	for _, id := range providerOrder {
		if !s.settings.providerEnabled(id) {
			continue
		}
		section := panelSection{provider: id, top: top}
		if id == providerCodex {
			cards, end := codexCards(s.quota, top)
			section.cards = cards
			section.height = end - panelCardGap - top
			section.connected = s.codexOK
		} else {
			section.ext = s.ext[id]
			section.email = s.settings.providerConfigFor(id).Email
			section.connected = section.ext.connected()
			section.height = extCardHeight(section.ext)
		}
		sections = append(sections, section)
		top += section.height + panelCardGap
	}
	if len(sections) == 0 {
		return nil, panelHeaderHeight + emptyPanelHintHeight + panelBottomMargin
	}
	return sections, top - panelCardGap + panelBottomMargin
}

func panelHeightForState(s *appState) int32 {
	_, height := sectionLayout(s)
	return height
}

// ---- card styles -----------------------------------------------------------

type providerStyle struct {
	accent, accent2, onAccent uint32
	radius                    int32 // GDI ellipse size, so the corner radius is half
	face                      string
	segmented, gradient       bool
	barHeight                 int32
	badge                     string
}

func styleFor(id string, dark bool) providerStyle {
	switch id {
	case providerClaude:
		accent := colorRef(196, 98, 63)
		if dark {
			accent = colorRef(217, 119, 87)
		}
		return providerStyle{accent: accent, accent2: accent, onAccent: colorRef(255, 255, 255),
			radius: 16, face: "Georgia", barHeight: 12, badge: "C"}
	case providerAntigravity:
		style := providerStyle{accent: colorRef(31, 134, 214), accent2: colorRef(15, 168, 155), onAccent: colorRef(255, 255, 255),
			radius: 12, face: "Microsoft YaHei UI", gradient: true, barHeight: 8, badge: "A"}
		if dark {
			style.accent, style.accent2, style.onAccent = colorRef(63, 169, 245), colorRef(39, 211, 196), colorRef(11, 18, 32)
		}
		return style
	default:
		accent := colorRef(109, 63, 224)
		if dark {
			accent = colorRef(139, 92, 246)
		}
		return providerStyle{accent: accent, accent2: accent, onAccent: colorRef(255, 255, 255),
			radius: 16, face: "Microsoft YaHei UI", barHeight: 12, badge: ">_"}
	}
}

// cardLook is a provider style plus whether the provider is connected; a
// disconnected card is drawn in neutral grey.
type cardLook struct {
	style     providerStyle
	connected bool
	id        string
}

func blendColor(a, b uint32, t float64) uint32 {
	mix := func(shift uint) uint32 {
		x, y := float64((a>>shift)&0xff), float64((b>>shift)&0xff)
		return uint32(math.Round(x+(y-x)*t)) & 0xff
	}
	return mix(0) | mix(8)<<8 | mix(16)<<16
}

func (l cardLook) cardFill(c panelColors) uint32 {
	if !l.connected {
		return c.card
	}
	return blendColor(c.card, l.style.accent, 0.09)
}

func (l cardLook) cardBorder(c panelColors) uint32 {
	if !l.connected {
		return c.border
	}
	return blendColor(c.border, l.style.accent, 0.45)
}

func (l cardLook) band(c panelColors) (uint32, uint32) {
	if !l.connected {
		return c.track, c.track
	}
	return l.style.accent, l.style.accent2
}

func (l cardLook) bandText(c panelColors) uint32 {
	if !l.connected {
		return c.muted
	}
	return l.style.onAccent
}

func (l cardLook) track(c panelColors) uint32 {
	if !l.connected {
		return c.track
	}
	return blendColor(l.cardFill(c), l.style.accent, 0.18)
}

// meterFill is the accent while the quota is healthy, then yellow below 30%
// and red below 10%, whatever the provider.
func (l cardLook) meterFill(c panelColors, percent int) (uint32, uint32) {
	switch {
	case !l.connected:
		grey := blendColor(c.track, c.muted, 0.45)
		return grey, grey
	case percent < 10:
		return c.danger, c.danger
	case percent < 30:
		return c.warn, c.warn
	}
	return l.style.accent, l.style.accent2
}

func fillRect(hdc uintptr, bounds rect, color uint32) {
	brush := createBrush(color)
	procFillRect.Call(hdc, uintptr(unsafe.Pointer(&bounds)), uintptr(brush))
	procDeleteObject.Call(uintptr(brush))
}

// drawGradientRound paints a rounded rectangle shaded from c1 (left) to c2
// (right) using vertical strips between two rounded end caps.
func drawGradientRound(hdc uintptr, b rect, radius int32, c1, c2 uint32, squareBottom bool) {
	if c1 == c2 {
		drawRoundRect(hdc, b, radius, c1, c1)
		if squareBottom {
			fillRect(hdc, rect{Left: b.Left, Top: b.Top + radius/2, Right: b.Right, Bottom: b.Bottom}, c1)
		}
		return
	}
	r := radius / 2
	width := b.Right - b.Left
	drawRoundRect(hdc, b, radius, c2, c2)
	drawRoundRect(hdc, rect{Left: b.Left, Top: b.Top, Right: (b.Left + b.Right) / 2, Bottom: b.Bottom}, radius, c1, c1)
	if squareBottom {
		fillRect(hdc, rect{Left: b.Left, Top: b.Top + r, Right: b.Left + r + 1, Bottom: b.Bottom}, c1)
		fillRect(hdc, rect{Left: b.Right - r - 1, Top: b.Top + r, Right: b.Right, Bottom: b.Bottom}, c2)
	}
	const step = 4
	for x := b.Left + r; x < b.Right-r; x += step {
		fillRect(hdc, rect{Left: x, Top: b.Top, Right: min(x+step, b.Right-r), Bottom: b.Bottom},
			blendColor(c1, c2, float64(x-b.Left)/float64(max(width, 1))))
	}
}

func drawDiamond(hdc uintptr, cx, cy, half int32, fill uint32) {
	points := [4]point{{X: cx, Y: cy - half}, {X: cx + half, Y: cy}, {X: cx, Y: cy + half}, {X: cx - half, Y: cy}}
	brush := createBrush(fill)
	pen, _, _ := procCreatePen.Call(0, 1, uintptr(fill))
	oldBrush, _, _ := procSelectObject.Call(hdc, uintptr(brush))
	oldPen, _, _ := procSelectObject.Call(hdc, pen)
	procPolygon.Call(hdc, uintptr(unsafe.Pointer(&points[0])), uintptr(len(points)))
	procSelectObject.Call(hdc, oldPen)
	procSelectObject.Call(hdc, oldBrush)
	procDeleteObject.Call(pen)
	procDeleteObject.Call(uintptr(brush))
}

// withFont runs draw with a temporary font selected into hdc.
func withFont(hdc uintptr, face string, height int32, weight int, draw func()) {
	font, _, _ := procCreateFont.Call(
		uintptr(height), 0, 0, 0, uintptr(weight),
		0, 0, 0, 1, 0, 0, 5, 0,
		uintptr(unsafe.Pointer(utf16Ptr(face))),
	)
	if font == 0 {
		draw()
		return
	}
	old, _, _ := procSelectObject.Call(hdc, font)
	draw()
	procSelectObject.Call(hdc, old)
	procDeleteObject.Call(font)
}

// drawCardShell draws the card body and its coloured title band with the
// provider badge, name and detail text (account, plan).
func drawCardShell(hdc uintptr, colors panelColors, look cardLook, top, height int32, detail string) {
	style := look.style
	card := rect{Left: 14, Top: top, Right: panelWidth - 14, Bottom: top + height}
	drawRoundRect(hdc, card, style.radius, look.cardFill(colors), look.cardBorder(colors))

	c1, c2 := look.band(colors)
	bandRect := rect{Left: card.Left + 1, Top: card.Top + 1, Right: card.Right - 1, Bottom: top + bandHeight}
	drawGradientRound(hdc, bandRect, style.radius, c1, c2, true)

	text := look.bandText(colors)
	badgeFill := blendColor(c1, text, 0.28)
	badge := rect{Left: 24, Top: top + 6, Right: 44, Bottom: top + 26}
	switch look.id {
	case providerClaude:
		drawRoundRect(hdc, badge, 20, badgeFill, badgeFill)
	case providerAntigravity:
		drawDiamond(hdc, 34, top+16, 11, badgeFill)
	default:
		drawRoundRect(hdc, badge, 12, badgeFill, badgeFill)
	}
	drawText(hdc, style.badge, badge, text, dtSingleLine|dtVCenter|dtCenter)

	withFont(hdc, style.face, -17, 700, func() {
		drawText(hdc, providerTitle(look.id), rect{Left: 52, Top: top + 1, Right: 162, Bottom: top + bandHeight}, text, dtSingleLine|dtVCenter)
	})
	drawText(hdc, detail, rect{Left: 166, Top: top + 1, Right: panelWidth - 25, Bottom: top + bandHeight}, text, dtSingleLine|dtVCenter|dtEndEllipsis)
}

func drawStyledMeter(hdc uintptr, label string, left, top int32, bucket quotaBucket, colors panelColors, look cardLook) {
	style := look.style
	drawText(hdc, label, rect{Left: left, Top: top - 4, Right: left + 54, Bottom: top + 16}, colors.muted, dtSingleLine|dtVCenter)
	barLeft, barRight := left+64, left+235
	barTop := top + (12-style.barHeight)/2
	barBottom := barTop + style.barHeight
	track := look.track(colors)
	percent := 0
	if bucket.Known {
		percent = bucket.Percent
	}
	fill1, fill2 := look.meterFill(colors, percent)
	filled := bucket.Known && percent > 0
	fillWidth := int32(0)
	if filled {
		fillWidth = max(int32(float64(barRight-barLeft)*float64(percent)/100.0), 4)
	}

	if style.segmented {
		const block, gap = 8, 2
		count := (barRight - barLeft + gap) / (block + gap)
		lit := int32(0)
		if filled {
			lit = max(int32(math.Ceil(float64(count)*float64(percent)/100.0)), 1)
		}
		for i := int32(0); i < count; i++ {
			x := barLeft + i*(block+gap)
			color := track
			if i < lit {
				color = fill1
			}
			fillRect(hdc, rect{Left: x, Top: barTop, Right: x + block, Bottom: barBottom}, color)
		}
	} else {
		drawRoundRect(hdc, rect{Left: barLeft, Top: barTop, Right: barRight, Bottom: barBottom}, 6, track, track)
		if filled {
			drawGradientRound(hdc, rect{Left: barLeft, Top: barTop, Right: barLeft + fillWidth, Bottom: barBottom}, 6, fill1, fill2, false)
		}
	}

	percentText, percentColor := bucketText(bucket), colors.text
	if bucket.Known && look.connected && percent < 30 {
		percentText = "! " + percentText
		percentColor, _ = look.meterFill(colors, percent)
	}
	drawText(hdc, percentText, rect{Left: barRight + 8, Top: top - 4, Right: barRight + 64, Bottom: top + 16}, percentColor, dtSingleLine|dtVCenter)
	drawText(hdc, quotaRemainingText(bucket, time.Now()), rect{Left: barRight + 72, Top: top - 4, Right: panelWidth - 24, Bottom: top + 16}, colors.muted, dtSingleLine|dtVCenter|dtEndEllipsis)
}

func drawExtCard(hdc uintptr, colors panelColors, section panelSection) {
	look := cardLook{style: styleFor(section.provider, colors.dark), connected: section.connected, id: section.provider}
	q := section.ext
	detail := q.Email
	if detail == "" {
		detail = section.email
	}
	if q.Plan != "" {
		detail += "  ·  " + q.Plan
	}
	if !q.Fetched {
		message := "未连接"
		if q.Err != "" {
			message += "  ·  " + shortError(q.Err)
		} else {
			message += "  ·  等待首次同步"
		}
		drawCardShell(hdc, colors, look, section.top, section.height, detail)
		drawText(hdc, message, rect{Left: 27, Top: section.top + bandHeight + 8, Right: panelWidth - 25, Bottom: section.top + section.height - 8}, colors.muted, dtSingleLine|dtVCenter|dtEndEllipsis)
		return
	}
	drawCardShell(hdc, colors, look, section.top, section.height, detail)
	y := section.top + bandHeight + 14
	groups := q.Groups
	if len(groups) == 0 {
		groups = []extGroup{{}}
	}
	for i, group := range groups {
		if len(q.Groups) > 1 {
			if i > 0 {
				y += extGroupGap
			}
			drawText(hdc, group.Name, rect{Left: 27, Top: y - 8, Right: panelWidth - 25, Bottom: y + 12}, colors.text, dtSingleLine|dtVCenter|dtEndEllipsis)
			y += extGroupHeaderHeight
		}
		drawStyledMeter(hdc, "5小时", 27, y, group.Primary, colors, look)
		drawStyledMeter(hdc, "周额度", 27, y+extRowPitch, group.Secondary, colors, look)
		y += 2 * extRowPitch
	}
}

// ---- configuration dialog --------------------------------------------------

const (
	classDialog = "QuotaTrayMonitorProviderDialog"

	idDialogOK      = 1 // 启用 (default button, Enter)
	idDialogCancel  = 2 // Esc / close
	idDialogDisable = 2103
	idDialogBase    = 2101
	idDialogEmail   = 2102

	dlgEnable  = 1
	dlgDisable = 2

	wsChild         = 0x40000000
	wsVisible       = 0x10000000
	wsTabStop       = 0x00010000
	wsGroup         = 0x00020000
	wsBorder        = 0x00800000
	wsCaption       = 0x00C00000
	wsSysMenu       = 0x00080000
	wsExClientEdge  = 0x00000200
	wsExDlgFrame    = 0x00000001
	esAutoHScroll   = 0x0080
	bsPushButton    = 0x0000
	bsDefPushButton = 0x0001
	wmSetFont       = 0x0030
	emSetSel        = 0x00B1
	colorBtnFace    = 15
)

type providerDialog struct {
	hwnd     syscall.Handle
	baseEdit syscall.Handle
	mailEdit syscall.Handle
	provider string
	current  providerConfig
	result   int
	baseURL  string
	email    string
	done     bool
}

var (
	procEnableWindow     = user32.NewProc("EnableWindow")
	procDestroyWindow    = user32.NewProc("DestroyWindow")
	procSetFocus         = user32.NewProc("SetFocus")
	procGetWindowText    = user32.NewProc("GetWindowTextW")
	procSetWindowText    = user32.NewProc("SetWindowTextW")
	procIsDialogMessage  = user32.NewProc("IsDialogMessageW")
	procAdjustWindowRect = user32.NewProc("AdjustWindowRectEx")
	procPolygon          = gdi32.NewProc("Polygon")
)

type winMessage struct {
	Window  syscall.Handle
	Message uint32
	WParam  uintptr
	LParam  uintptr
	Time    uint32
	Point   point
	Private uint32
}

func registerDialogClass(instance syscall.Handle, callback uintptr, cursor syscall.Handle) error {
	wc := wndClassEx{
		Size:       uint32(unsafe.Sizeof(wndClassEx{})),
		WndProc:    callback,
		Instance:   instance,
		Cursor:     cursor,
		Background: syscall.Handle(colorBtnFace + 1),
		ClassName:  utf16Ptr(classDialog),
	}
	if r, _, _ := procRegisterClassEx.Call(uintptr(unsafe.Pointer(&wc))); r == 0 {
		return winError("RegisterClassExW (dialog)")
	}
	return nil
}

func windowText(hwnd syscall.Handle) string {
	buffer := make([]uint16, 1024)
	n, _, _ := procGetWindowText.Call(uintptr(hwnd), uintptr(unsafe.Pointer(&buffer[0])), uintptr(len(buffer)))
	return strings.TrimSpace(syscall.UTF16ToString(buffer[:n]))
}

func (a *monitorApp) createDialogControl(class, text string, style, exStyle uintptr, x, y, w, h int32, parent syscall.Handle, id uintptr, font uintptr) syscall.Handle {
	control, _, _ := procCreateWindowEx.Call(
		exStyle,
		uintptr(unsafe.Pointer(utf16Ptr(class))),
		uintptr(unsafe.Pointer(utf16Ptr(text))),
		style|wsChild|wsVisible,
		uintptr(a.scale(x)), uintptr(a.scale(y)), uintptr(a.scale(w)), uintptr(a.scale(h)),
		uintptr(parent), id, uintptr(a.instance), 0,
	)
	if control != 0 && font != 0 {
		procSendMessage.Call(control, wmSetFont, font, 1)
	}
	return syscall.Handle(control)
}

// editProvider shows the base_url / email dialog for one provider and applies
// the user's choice. It runs its own message loop, so the caller (the tray
// menu handler) is blocked until the dialog closes.
func (a *monitorApp) editProvider(id string) {
	if a.dialog != nil {
		procSetForegroundWindow.Call(uintptr(a.dialog.hwnd))
		return
	}
	current := a.snapshot().settings.providerConfigFor(id)
	dlg := &providerDialog{provider: id, current: current}

	const clientW, clientH = 400, 196
	style := uintptr(wsPopup | wsCaption | wsSysMenu)
	exStyle := uintptr(wsExDlgFrame | wsExToolWindow | wsExTopmost)
	frame := rect{Right: a.scale(clientW), Bottom: a.scale(clientH)}
	procAdjustWindowRect.Call(uintptr(unsafe.Pointer(&frame)), style, 0, exStyle)
	width, height := frame.Right-frame.Left, frame.Bottom-frame.Top
	screenW, _, _ := procGetSystemMetrics.Call(0)
	screenH, _, _ := procGetSystemMetrics.Call(1)

	hwnd, _, _ := procCreateWindowEx.Call(
		exStyle,
		uintptr(unsafe.Pointer(utf16Ptr(classDialog))),
		uintptr(unsafe.Pointer(utf16Ptr("配置 "+providerTitle(id)))),
		style,
		uintptr((int32(screenW)-width)/2), uintptr((int32(screenH)-height)/2), uintptr(width), uintptr(height),
		uintptr(a.mainWindow), 0, uintptr(a.instance), 0,
	)
	if hwnd == 0 {
		a.queueAlert("无法打开配置对话框："+winError("CreateWindowExW (dialog)").Error(), mbIconWarning)
		return
	}
	dlg.hwnd = syscall.Handle(hwnd)

	font, _, _ := procCreateFont.Call(
		uintptr(-a.scale(16)), 0, 0, 0, 400,
		0, 0, 0, 1, 0, 0, 5, 0,
		uintptr(unsafe.Pointer(utf16Ptr("Microsoft YaHei UI"))),
	)
	baseHint := "base_url（例如 cpa.example.com）"
	if id == providerCodex {
		baseHint = "base_url（留空使用默认地址）"
	}
	a.createDialogControl("STATIC", baseHint, 0, 0, 16, 14, 368, 20, dlg.hwnd, 0, font)
	dlg.baseEdit = a.createDialogControl("EDIT", current.BaseURL, wsTabStop|wsBorder|esAutoHScroll, wsExClientEdge, 16, 36, 368, 26, dlg.hwnd, idDialogBase, font)
	emailHint := "email"
	if id == providerCodex {
		emailHint = "email（Codex 请求暂不使用，仅作记录）"
	}
	a.createDialogControl("STATIC", emailHint, 0, 0, 16, 74, 368, 20, dlg.hwnd, 0, font)
	dlg.mailEdit = a.createDialogControl("EDIT", current.Email, wsTabStop|wsBorder|esAutoHScroll, wsExClientEdge, 16, 96, 368, 26, dlg.hwnd, idDialogEmail, font)
	a.createDialogControl("BUTTON", "启用", wsTabStop|bsDefPushButton, 0, 196, 148, 90, 30, dlg.hwnd, idDialogOK, font)
	a.createDialogControl("BUTTON", "禁用", wsTabStop|bsPushButton, 0, 294, 148, 90, 30, dlg.hwnd, idDialogDisable, font)

	a.dialog = dlg
	a.modalDepth++
	procEnableWindow.Call(uintptr(a.panelWindow), 0)
	procShowWindow.Call(hwnd, swShow)
	procSetForegroundWindow.Call(hwnd)
	procSetFocus.Call(uintptr(dlg.baseEdit))
	procSendMessage.Call(uintptr(dlg.baseEdit), emSetSel, 0, ^uintptr(0))

	var msg winMessage
	for !dlg.done {
		r, _, _ := procGetMessage.Call(uintptr(unsafe.Pointer(&msg)), 0, 0, 0)
		if int32(r) <= 0 {
			if r == 0 {
				procPostQuitMessage.Call(msg.WParam)
			}
			break
		}
		if handled, _, _ := procIsDialogMessage.Call(hwnd, uintptr(unsafe.Pointer(&msg))); handled != 0 {
			continue
		}
		procTranslateMessage.Call(uintptr(unsafe.Pointer(&msg)))
		procDispatchMessage.Call(uintptr(unsafe.Pointer(&msg)))
	}

	procEnableWindow.Call(uintptr(a.panelWindow), 1)
	a.modalDepth--
	a.dialog = nil
	if font != 0 {
		procDeleteObject.Call(font)
	}
	if dlg.done && dlg.result != 0 {
		a.applyProviderDialog(id, dlg)
	}
}

// handleDialogCommand reacts to the dialog's buttons. An invalid "启用" keeps
// the dialog open with an explanation.
func (a *monitorApp) handleDialogCommand(dlg *providerDialog, command uint16) {
	switch command {
	case idDialogOK, idDialogDisable:
		dlg.baseURL = windowText(dlg.baseEdit)
		dlg.email = windowText(dlg.mailEdit)
		if command == idDialogOK {
			cfg, err := validateProviderInput(dlg.provider, dlg.baseURL, dlg.email)
			if err != nil {
				a.messageBox(dlg.hwnd, err.Error(), mbOK|mbIconWarning)
				return
			}
			dlg.baseURL, dlg.email, dlg.result = cfg.BaseURL, cfg.Email, dlgEnable
		} else {
			// Disabling keeps whatever was typed if it is usable, else the old values.
			dlg.result = dlgDisable
			if cfg, err := validateProviderInput(dlg.provider, dlg.baseURL, dlg.email); err == nil {
				dlg.baseURL, dlg.email = cfg.BaseURL, cfg.Email
			} else {
				dlg.baseURL, dlg.email = dlg.current.BaseURL, dlg.current.Email
			}
		}
	case idDialogCancel:
		dlg.result = 0
	default:
		return
	}
	dlg.done = true
	procDestroyWindow.Call(uintptr(dlg.hwnd))
}

func (a *monitorApp) applyProviderDialog(id string, dlg *providerDialog) {
	cfg := providerConfig{Enabled: dlg.result == dlgEnable, BaseURL: dlg.baseURL, Email: dlg.email}
	a.state.mu.Lock()
	changed := cfg.BaseURL != dlg.current.BaseURL || !strings.EqualFold(cfg.Email, dlg.current.Email)
	a.state.settings = a.state.settings.withProvider(id, cfg)
	if changed {
		if id == providerCodex {
			a.state.codexOK = false
		} else {
			delete(a.state.ext, id)
		}
	}
	settings := a.state.settings
	a.state.mu.Unlock()
	saveSettings(settings)
	select {
	case a.quotaChanged <- struct{}{}:
	default:
	}
	procPostMessage.Call(uintptr(a.mainWindow), wmStateChanged, 0, 0)
	if cfg.Enabled {
		a.startFetch(true)
	}
	a.showPanel()
}
