//go:build windows

package main

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unicode/utf16"
	"unsafe"
)

const (
	apiURL              = "https://kot.xkcht.eu.org/v0/resource/plugins/user-routing/quota"
	directAPIURL        = "https://kot.xkcht.eu.org/v0/resource/plugins/user-routing/quota/direct"
	resetAPIURL         = "https://kot.xkcht.eu.org/v0/resource/plugins/user-routing/quota/reset"
	directResetAPIURL   = "https://kot.xkcht.eu.org/v0/resource/plugins/user-routing/quota/direct/reset"
	authRelPath         = ".codex\\auth.json"
	authModeAPIKey      = "apikey"
	authModeChatGPT     = "chatgpt"
	quotaRequestRetries = 5

	classMain  = "QuotaTrayMonitorMainWindow"
	classPanel = "QuotaTrayMonitorPanelWindow"
	windowName = "Codex额度监控"

	wmApp             = 0x8000
	wmTray            = wmApp + 10
	wmStateChanged    = wmApp + 11
	wmResetCompleted  = wmApp + 12
	wmActivate        = 0x0006
	wmPaint           = 0x000F
	wmClose           = 0x0010
	wmEraseBackground = 0x0014
	wmSettingChange   = 0x001A
	wmThemeChanged    = 0x031A
	wmCommand         = 0x0111
	wmTimer           = 0x0113
	wmLButtonDown     = 0x0201
	wmExitSizeMove    = 0x0232
	wmNCLButtonDown   = 0x00A1
	wmNull            = 0x0000
	wmLButtonUp       = 0x0202
	wmRButtonUp       = 0x0205
	wmContextMenu     = 0x007B
	waInactive        = 0
	htCaption         = 2

	wsPopup          = 0x80000000
	wsExToolWindow   = 0x00000080
	wsExTopmost      = 0x00000008
	swHide           = 0
	swShow           = 5
	swpNoMove        = 0x0002
	swpNoSize        = 0x0001
	swpNoZOrder      = 0x0004
	swpNoActivate    = 0x0010
	swpShowWindow    = 0x0040
	tpmRightButton   = 0x0002
	tpmReturnCommand = 0x0100

	nimAdd            = 0
	nimModify         = 1
	nimDelete         = 2
	nimSetVer         = 4
	nifMessage        = 1
	nifIcon           = 2
	nifTip            = 4
	iconID            = 1
	menuRefresh       = 1001
	menuOpenPanel     = 1002
	menuTopmost       = 1003
	menuStartup       = 1004
	menuExit          = 1005
	menuRefresh10     = 1010
	menuRefresh20     = 1011
	menuRefresh30     = 1012
	menuRefresh60     = 1013
	menuCheckUpdates  = 1014
	menuInstallUpdate = 1015

	mbOK              = 0x00000000
	mbYesNo           = 0x00000004
	mbIconInformation = 0x00000040
	mbIconWarning     = 0x00000030
	mbDefaultButton2  = 0x00000100
	idYes             = 6

	dibRGBColors        = 0
	biRGB               = 0
	monDefaultToNearest = 2
	dtSingleLine        = 0x0020
	dtVCenter           = 0x0004
	dtCenter            = 0x0001
	dtEndEllipsis       = 0x8000

	hwndTop       = 0
	hwndTopmost   = ^uintptr(0)
	hwndNotopmost = ^uintptr(1)

	hkeyCurrentUser    = 0x80000001
	keySetValue        = 0x0002
	keyQueryValue      = 0x0001
	regSZ              = 1
	regDword           = 4
	errorFileNotFound  = 2
	errorAlreadyExists = 183
	panelTimerID       = 1
	panelTimerInterval = 60_000
)

const (
	themeSystem = "system"
	themeLight  = "light"
	themeDark   = "dark"
)

var (
	user32   = syscall.NewLazyDLL("user32.dll")
	shell32  = syscall.NewLazyDLL("shell32.dll")
	gdi32    = syscall.NewLazyDLL("gdi32.dll")
	kernel32 = syscall.NewLazyDLL("kernel32.dll")
	advapi32 = syscall.NewLazyDLL("advapi32.dll")

	procRegisterClassEx        = user32.NewProc("RegisterClassExW")
	procCreateWindowEx         = user32.NewProc("CreateWindowExW")
	procDefWindowProc          = user32.NewProc("DefWindowProcW")
	procMessageBox             = user32.NewProc("MessageBoxW")
	procGetMessage             = user32.NewProc("GetMessageW")
	procTranslateMessage       = user32.NewProc("TranslateMessage")
	procDispatchMessage        = user32.NewProc("DispatchMessageW")
	procPostMessage            = user32.NewProc("PostMessageW")
	procPostQuitMessage        = user32.NewProc("PostQuitMessage")
	procShowWindow             = user32.NewProc("ShowWindow")
	procSetTimer               = user32.NewProc("SetTimer")
	procKillTimer              = user32.NewProc("KillTimer")
	procSetWindowPos           = user32.NewProc("SetWindowPos")
	procSendMessage            = user32.NewProc("SendMessageW")
	procReleaseCapture         = user32.NewProc("ReleaseCapture")
	procGetWindowRect          = user32.NewProc("GetWindowRect")
	procSetForegroundWindow    = user32.NewProc("SetForegroundWindow")
	procGetCursorPos           = user32.NewProc("GetCursorPos")
	procMonitorFromPoint       = user32.NewProc("MonitorFromPoint")
	procGetMonitorInfo         = user32.NewProc("GetMonitorInfoW")
	procBeginPaint             = user32.NewProc("BeginPaint")
	procEndPaint               = user32.NewProc("EndPaint")
	procDrawText               = user32.NewProc("DrawTextW")
	procSetTextColor           = gdi32.NewProc("SetTextColor")
	procSetBkMode              = gdi32.NewProc("SetBkMode")
	procCreateFont             = gdi32.NewProc("CreateFontW")
	procCreateSolidBrush       = gdi32.NewProc("CreateSolidBrush")
	procCreatePen              = gdi32.NewProc("CreatePen")
	procSelectObject           = gdi32.NewProc("SelectObject")
	procDeleteObject           = gdi32.NewProc("DeleteObject")
	procFillRect               = user32.NewProc("FillRect")
	procRoundRect              = gdi32.NewProc("RoundRect")
	procMoveToEx               = gdi32.NewProc("MoveToEx")
	procLineTo                 = gdi32.NewProc("LineTo")
	procShellNotifyIcon        = shell32.NewProc("Shell_NotifyIconW")
	procShellNotifyIconGetRect = shell32.NewProc("Shell_NotifyIconGetRect")
	procRegisterWindowMessage  = user32.NewProc("RegisterWindowMessageW")
	procGetModuleHandle        = kernel32.NewProc("GetModuleHandleW")
	procCreateMutex            = kernel32.NewProc("CreateMutexW")
	procCloseHandle            = kernel32.NewProc("CloseHandle")
	procCreateDIBSection       = gdi32.NewProc("CreateDIBSection")
	procCreateBitmap           = gdi32.NewProc("CreateBitmap")
	procCreateIconIndirect     = user32.NewProc("CreateIconIndirect")
	procDestroyIcon            = user32.NewProc("DestroyIcon")
	procGetDC                  = user32.NewProc("GetDC")
	procReleaseDC              = user32.NewProc("ReleaseDC")
	procCreatePopupMenu        = user32.NewProc("CreatePopupMenu")
	procAppendMenu             = user32.NewProc("AppendMenuW")
	procCheckMenuItem          = user32.NewProc("CheckMenuItem")
	procTrackPopupMenu         = user32.NewProc("TrackPopupMenu")
	procDestroyMenu            = user32.NewProc("DestroyMenu")
	procRegCreateKeyEx         = advapi32.NewProc("RegCreateKeyExW")
	procRegOpenKeyEx           = advapi32.NewProc("RegOpenKeyExW")
	procRegSetValueEx          = advapi32.NewProc("RegSetValueExW")
	procRegDeleteValue         = advapi32.NewProc("RegDeleteValueW")
	procRegQueryValueEx        = advapi32.NewProc("RegQueryValueExW")
	procRegCloseKey            = advapi32.NewProc("RegCloseKey")
)

type point struct {
	X int32
	Y int32
}

type rect struct {
	Left   int32
	Top    int32
	Right  int32
	Bottom int32
}

type paintStruct struct {
	HDC       syscall.Handle
	Erase     int32
	Paint     rect
	Restore   int32
	IncUpdate int32
	Reserved  [32]byte
}

type wndClassEx struct {
	Size       uint32
	Style      uint32
	WndProc    uintptr
	ClsExtra   int32
	WndExtra   int32
	Instance   syscall.Handle
	Icon       syscall.Handle
	Cursor     syscall.Handle
	Background syscall.Handle
	MenuName   *uint16
	ClassName  *uint16
	IconSmall  syscall.Handle
}

type notifyIconData struct {
	Size             uint32
	Window           syscall.Handle
	ID               uint32
	Flags            uint32
	CallbackMessage  uint32
	Icon             syscall.Handle
	Tooltip          [128]uint16
	State            uint32
	StateMask        uint32
	Info             [256]uint16
	TimeoutOrVersion uint32
	InfoTitle        [64]uint16
	InfoFlags        uint32
	GUID             [16]byte
	BalloonIcon      syscall.Handle
}

type bitmapInfoHeader struct {
	Size            uint32
	Width           int32
	Height          int32
	Planes          uint16
	BitCount        uint16
	Compression     uint32
	SizeImage       uint32
	XPixelsPerM     int32
	YPixelsPerM     int32
	ColorsUsed      uint32
	ColorsImportant uint32
}

type bitmapInfo struct {
	Header bitmapInfoHeader
	Color  [1]uint32
}

type iconInfo struct {
	Icon     int32
	HotspotX uint32
	HotspotY uint32
	Mask     syscall.Handle
	Color    syscall.Handle
}

type monitorInfo struct {
	Size    uint32
	Monitor rect
	Work    rect
	Flags   uint32
}

type quotaBucket struct {
	Percent int
	Known   bool
	ResetAt time.Time
}

type accountQuota struct {
	Email     string
	Primary   quotaBucket
	Secondary quotaBucket
	Reset     resetCreditInfo
}

type resetCreditInfo struct {
	AvailableCount         int      `json:"available_count"`
	ExpiresAt              []string `json:"expires_at"`
	WithoutExpiry          int      `json:"without_expiry"`
	ExpiryDetailsAvailable bool     `json:"expiry_details_available"`
	ExpiryDetailsComplete  bool     `json:"expiry_details_complete"`
}

type quotaSnapshot struct {
	Nominal          accountQuota
	Actual           accountQuota
	NominalAvailable bool
	ActualAvailable  bool
	SingleAccount    bool
	DirectMode       bool
	ResetSupported   bool
}

type appSettings struct {
	AlwaysOnTop    bool
	PositionSet    bool
	PanelX         int32
	PanelY         int32
	RefreshMinutes int
	ThemeMode      string
}

type appState struct {
	mu              sync.RWMutex
	quota           quotaSnapshot
	status          string
	updated         time.Time
	settings        appSettings
	startup         bool
	resetResult     string
	updateStatus    string
	updateChecking  bool
	updateAvailable bool
	updateRelease   updateRelease
}

type monitorApp struct {
	state            appState
	mainWindow       syscall.Handle
	panelWindow      syscall.Handle
	instance         syscall.Handle
	mutex            syscall.Handle
	trayAdded        bool
	panelVisible     bool
	trayIcon         syscall.Handle
	taskbarMsg       uint32
	fetching         atomic.Bool
	resetPending     atomic.Bool
	checkingUpdate   atomic.Bool
	installingUpdate atomic.Bool
	intervalChange   chan time.Duration
	panelButton      string
	menuOpen         bool
}

var currentApp *monitorApp

func main() {
	if err := run(); err != nil {
		showError(err.Error())
	}
}

func run() error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	name, _ := syscall.UTF16PtrFromString("Local\\CodexQuotaTrayMonitor")
	mutex, _, createErr := procCreateMutex.Call(0, 0, uintptr(unsafe.Pointer(name)))
	if mutex == 0 {
		return errors.New("无法创建单实例互斥锁")
	}
	if createErr == syscall.Errno(errorAlreadyExists) {
		procCloseHandle.Call(mutex)
		return nil
	}

	app := &monitorApp{mutex: syscall.Handle(mutex), intervalChange: make(chan time.Duration, 1)}
	currentApp = app
	app.state.settings = loadSettings()
	app.state.startup = startupEnabled()
	app.state.status = "等待首次同步"
	if executable, err := os.Executable(); err == nil {
		app.state.updateStatus = consumeUpdateFailure(executable)
	}

	instance, _, _ := procGetModuleHandle.Call(0)
	if instance == 0 {
		return winError("GetModuleHandleW")
	}
	app.instance = syscall.Handle(instance)

	mainClass, _ := syscall.UTF16PtrFromString(classMain)
	panelClass, _ := syscall.UTF16PtrFromString(classPanel)
	callback := syscall.NewCallback(windowProc)
	if err := registerWindowClass(mainClass, app.instance, callback); err != nil {
		return err
	}
	if err := registerWindowClass(panelClass, app.instance, callback); err != nil {
		return err
	}

	title, _ := syscall.UTF16PtrFromString(windowName)
	hwnd, _, _ := procCreateWindowEx.Call(
		wsExToolWindow,
		uintptr(unsafe.Pointer(mainClass)),
		uintptr(unsafe.Pointer(title)),
		wsPopup,
		0, 0, 0, 0,
		0, 0,
		uintptr(app.instance),
		0,
	)
	if hwnd == 0 {
		return winError("CreateWindowExW (tray)")
	}
	app.mainWindow = syscall.Handle(hwnd)

	panelEx := uintptr(wsExToolWindow)
	if app.state.settings.AlwaysOnTop {
		panelEx |= wsExTopmost
	}
	panel, _, _ := procCreateWindowEx.Call(
		panelEx,
		uintptr(unsafe.Pointer(panelClass)),
		uintptr(unsafe.Pointer(title)),
		wsPopup,
		0, 0, panelWidth, uintptr(panelHeightFor(quotaSnapshot{})),
		hwnd, 0,
		uintptr(app.instance),
		0,
	)
	if panel == 0 {
		return winError("CreateWindowExW (panel)")
	}
	app.panelWindow = syscall.Handle(panel)

	taskbarName, _ := syscall.UTF16PtrFromString("TaskbarCreated")
	taskbarMsg, _, _ := procRegisterWindowMessage.Call(uintptr(unsafe.Pointer(taskbarName)))
	app.taskbarMsg = uint32(taskbarMsg)

	if err := app.addTrayIcon(); err != nil {
		return err
	}
	app.startFetch()
	go app.refreshLoop()
	go app.updateCheckLoop()
	return app.messageLoop()
}

func registerWindowClass(className *uint16, instance syscall.Handle, callback uintptr) error {
	wc := wndClassEx{
		Size:      uint32(unsafe.Sizeof(wndClassEx{})),
		WndProc:   callback,
		Instance:  instance,
		ClassName: className,
	}
	r, _, _ := procRegisterClassEx.Call(uintptr(unsafe.Pointer(&wc)))
	if r == 0 {
		return winError("RegisterClassExW")
	}
	return nil
}

func (a *monitorApp) messageLoop() error {
	var msg struct {
		Window  syscall.Handle
		Message uint32
		WParam  uintptr
		LParam  uintptr
		Time    uint32
		Point   point
		Private uint32
	}
	for {
		r, _, _ := procGetMessage.Call(uintptr(unsafe.Pointer(&msg)), 0, 0, 0)
		if int32(r) == -1 {
			return winError("GetMessageW")
		}
		if r == 0 {
			return nil
		}
		procTranslateMessage.Call(uintptr(unsafe.Pointer(&msg)))
		procDispatchMessage.Call(uintptr(unsafe.Pointer(&msg)))
	}
}

func windowProc(hwnd uintptr, message uint32, wParam, lParam uintptr) uintptr {
	a := currentApp
	if a == nil {
		r, _, _ := procDefWindowProc.Call(hwnd, uintptr(message), wParam, lParam)
		return r
	}
	handle := syscall.Handle(hwnd)

	if message == a.taskbarMsg && a.taskbarMsg != 0 && handle == a.mainWindow {
		a.trayAdded = false
		_ = a.addTrayIcon()
		return 0
	}
	if handle == a.mainWindow {
		switch message {
		case wmTray:
			switch uint32(lParam) {
			case wmLButtonUp:
				if a.panelVisible {
					a.hidePanel()
				} else {
					a.showPanel()
				}
				return 0
			case wmRButtonUp, wmContextMenu:
				a.showMenu()
				return 0
			}
		case wmStateChanged:
			a.updateTray()
			if a.panelVisible {
				a.resizePanel()
				procInvalidateRect.Call(uintptr(a.panelWindow), 0, 1)
			}
			return 0
		case wmResetCompleted:
			a.state.mu.Lock()
			result := a.state.resetResult
			a.state.resetResult = ""
			a.state.mu.Unlock()
			if result != "" {
				flags := uintptr(mbOK | mbIconInformation)
				if strings.Contains(result, "未能") || strings.Contains(result, "不确定") || strings.Contains(result, "失败") {
					flags = mbOK | mbIconWarning
				}
				showMessageBox(a.mainWindow, result, flags)
			}
			a.resetPending.Store(false)
			a.fetching.Store(false)
			if result != "" {
				a.startFetch()
			}
			return 0
		case wmCommand:
			a.handleMenuCommand(uint16(wParam))
			return 0
		case wmClose:
			a.shutdown()
			return 0
		}
	}
	if handle == a.panelWindow {
		switch message {
		case wmLButtonDown:
			pt := pointFromLParam(lParam)
			a.panelButton = a.panelButtonAt(pt)
			if a.panelButton != "" {
				return 0
			}
			procReleaseCapture.Call()
			procSendMessage.Call(hwnd, wmNCLButtonDown, htCaption, 0)
			return 0
		case wmLButtonUp:
			button := a.panelButton
			a.panelButton = ""
			if button != "" && button == a.panelButtonAt(pointFromLParam(lParam)) {
				a.handlePanelButton(button)
			}
			return 0
		case wmRButtonUp, wmContextMenu:
			a.showMenu()
			return 0
		case wmExitSizeMove:
			a.savePanelPosition()
			return 0
		case wmTimer:
			if wParam == panelTimerID {
				procInvalidateRect.Call(uintptr(a.panelWindow), 0, 1)
				return 0
			}
		case wmSettingChange, wmThemeChanged:
			procInvalidateRect.Call(uintptr(a.panelWindow), 0, 1)
			return 0
		case wmPaint:
			a.paintPanel(handle)
			return 0
		case wmEraseBackground:
			return 1
		case wmActivate:
			if uint16(wParam) == waInactive && !a.menuOpen && !a.snapshot().settings.AlwaysOnTop {
				a.hidePanel()
				return 0
			}
		case wmClose:
			a.hidePanel()
			return 0
		}
	}
	r, _, _ := procDefWindowProc.Call(hwnd, uintptr(message), wParam, lParam)
	return r
}

func (a *monitorApp) startFetch() {
	if !a.fetching.CompareAndSwap(false, true) {
		return
	}
	a.state.mu.Lock()
	a.state.status = "正在刷新额度…"
	a.state.mu.Unlock()
	go func() {
		defer a.fetching.Store(false)
		quota, err := fetchQuota()
		a.state.mu.Lock()
		if err != nil {
			a.state.status = "同步失败：" + shortError(err.Error())
		} else {
			a.state.quota = quota
			a.state.updated = time.Now()
			a.state.status = "更新于 " + a.state.updated.Format("15:04:05")
		}
		a.state.mu.Unlock()
		procPostMessage.Call(uintptr(a.mainWindow), wmStateChanged, 0, 0)
	}()
}

func (a *monitorApp) refreshLoop() {
	interval := time.Duration(a.snapshot().settings.RefreshMinutes) * time.Minute
	timer := time.NewTimer(interval)
	defer timer.Stop()
	for {
		select {
		case <-timer.C:
			a.startFetch()
			timer.Reset(time.Duration(a.snapshot().settings.RefreshMinutes) * time.Minute)
		case interval = <-a.intervalChange:
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timer.Reset(interval)
		}
	}
}

func (a *monitorApp) addTrayIcon() error {
	icon := createTrayIcon(trayAccount(a.snapshot().quota))
	if icon == 0 {
		return winError("CreateIconIndirect")
	}
	nid := a.notifyData(icon)
	action := uintptr(nimAdd)
	if a.trayAdded {
		action = nimModify
	}
	r, _, _ := procShellNotifyIcon.Call(action, uintptr(unsafe.Pointer(&nid)))
	if r == 0 {
		procDestroyIcon.Call(uintptr(icon))
		return winError("Shell_NotifyIconW")
	}
	a.trayAdded = true
	old := a.trayIcon
	a.trayIcon = icon
	if old != 0 {
		procDestroyIcon.Call(uintptr(old))
	}
	return nil
}

func (a *monitorApp) updateTray() {
	if !a.trayAdded {
		return
	}
	_ = a.addTrayIcon()
}

func (a *monitorApp) notifyData(icon syscall.Handle) notifyIconData {
	nid := notifyIconData{
		Size:            uint32(unsafe.Sizeof(notifyIconData{})),
		Window:          a.mainWindow,
		ID:              iconID,
		Flags:           nifMessage | nifIcon | nifTip,
		CallbackMessage: wmTray,
		Icon:            icon,
	}
	s := a.tooltip()
	u16 := utf16.Encode([]rune(s))
	if len(u16) > len(nid.Tooltip)-1 {
		u16 = u16[:len(nid.Tooltip)-1]
	}
	copy(nid.Tooltip[:], u16)
	return nid
}

func (a *monitorApp) tooltip() string {
	s := a.snapshot()
	account := trayAccount(s.quota)
	name := account.Email
	if name == "" {
		name = "实际账户"
	}
	result := fmt.Sprintf("%s\n5小时剩余：%s  周额度剩余：%s", name, bucketText(account.Primary), bucketText(account.Secondary))
	if strings.HasPrefix(s.status, "同步失败") {
		result += "\n" + s.status
	}
	if s.updateAvailable {
		result += "\n可更新到 " + s.updateRelease.Tag
	} else if s.updateStatus != "" {
		result += "\n" + s.updateStatus
	}
	return result
}

func trayAccount(quota quotaSnapshot) accountQuota {
	if quota.NominalAvailable && (quota.SingleAccount || !quota.ActualAvailable) {
		return quota.Nominal
	}
	if quota.ActualAvailable {
		return quota.Actual
	}
	if quota.NominalAvailable {
		return quota.Nominal
	}
	return quota.Actual
}

func (a *monitorApp) snapshot() appState {
	a.state.mu.RLock()
	defer a.state.mu.RUnlock()
	return appState{
		quota:           a.state.quota,
		status:          a.state.status,
		updated:         a.state.updated,
		settings:        a.state.settings,
		startup:         a.state.startup,
		updateStatus:    a.state.updateStatus,
		updateChecking:  a.state.updateChecking,
		updateAvailable: a.state.updateAvailable,
		updateRelease:   a.state.updateRelease,
	}
}

func (a *monitorApp) showPanel() {
	if a.panelWindow == 0 {
		return
	}
	snapshot := a.snapshot()
	settings := snapshot.settings
	height := panelHeightFor(snapshot.quota)
	var x, y int32
	if settings.PositionSet {
		x, y = clampPanelPosition(settings.PanelX, settings.PanelY, height)
	} else {
		pt := point{}
		procGetCursorPos.Call(uintptr(unsafe.Pointer(&pt)))
		x, y = panelPosition(pt, height)
	}
	insertAfter := uintptr(hwndTop)
	if settings.AlwaysOnTop {
		insertAfter = hwndTopmost
	}
	procSetWindowPos.Call(
		uintptr(a.panelWindow), insertAfter,
		uintptr(x), uintptr(y), panelWidth, uintptr(height),
		swpShowWindow,
	)
	procShowWindow.Call(uintptr(a.panelWindow), swShow)
	procSetTimer.Call(uintptr(a.panelWindow), panelTimerID, panelTimerInterval, 0)
	a.panelVisible = true
	procInvalidateRect.Call(uintptr(a.panelWindow), 0, 1)
}

func (a *monitorApp) resizePanel() {
	if a.panelWindow == 0 || !a.panelVisible {
		return
	}
	bounds := rect{}
	if r, _, _ := procGetWindowRect.Call(uintptr(a.panelWindow), uintptr(unsafe.Pointer(&bounds))); r == 0 {
		return
	}
	height := panelHeightFor(a.snapshot().quota)
	x, y := clampPanelPosition(bounds.Left, bounds.Top, height)
	procSetWindowPos.Call(uintptr(a.panelWindow), 0, uintptr(x), uintptr(y), panelWidth, uintptr(height), swpNoActivate|swpNoZOrder)
}

func (a *monitorApp) savePanelPosition() {
	bounds := rect{}
	r, _, _ := procGetWindowRect.Call(uintptr(a.panelWindow), uintptr(unsafe.Pointer(&bounds)))
	if r == 0 {
		return
	}
	a.state.mu.Lock()
	a.state.settings.PanelX = bounds.Left
	a.state.settings.PanelY = bounds.Top
	a.state.settings.PositionSet = true
	settings := a.state.settings
	a.state.mu.Unlock()
	saveSettings(settings)
}

func (a *monitorApp) hidePanel() {
	if a.panelWindow != 0 {
		procKillTimer.Call(uintptr(a.panelWindow), panelTimerID)
		procShowWindow.Call(uintptr(a.panelWindow), swHide)
	}
	a.panelVisible = false
}

func panelPosition(pt point, height int32) (int32, int32) {
	monitor, _, _ := procMonitorFromPoint.Call(*(*uintptr)(unsafe.Pointer(&pt)), monDefaultToNearest)
	mi := monitorInfo{Size: uint32(unsafe.Sizeof(monitorInfo{}))}
	procGetMonitorInfo.Call(monitor, uintptr(unsafe.Pointer(&mi)))
	x := pt.X - panelWidth/2
	y := pt.Y - height - 8
	if y < mi.Work.Top {
		y = pt.Y + 8
	}
	return clampToWorkArea(x, y, height, mi.Work)
}

func clampPanelPosition(x, y, height int32) (int32, int32) {
	center := point{X: x + panelWidth/2, Y: y + height/2}
	monitor, _, _ := procMonitorFromPoint.Call(*(*uintptr)(unsafe.Pointer(&center)), monDefaultToNearest)
	mi := monitorInfo{Size: uint32(unsafe.Sizeof(monitorInfo{}))}
	if r, _, _ := procGetMonitorInfo.Call(monitor, uintptr(unsafe.Pointer(&mi))); r == 0 {
		return x, y
	}
	return clampToWorkArea(x, y, height, mi.Work)
}

func clampToWorkArea(x, y, height int32, work rect) (int32, int32) {
	if x < work.Left {
		x = work.Left
	}
	if x+panelWidth > work.Right {
		x = work.Right - panelWidth
	}
	if y < work.Top {
		y = work.Top
	}
	if y+height > work.Bottom {
		y = work.Bottom - height
	}
	return x, y
}

func (a *monitorApp) showMenu() {
	menu, _, _ := procCreatePopupMenu.Call()
	if menu == 0 {
		return
	}
	defer procDestroyMenu.Call(menu)
	a.menuOpen = true
	appendMenu(menu, 0, menuRefresh, "立即刷新")
	appendMenu(menu, 0, menuCheckUpdates, "检查更新")
	if snapshot := a.snapshot(); snapshot.updateAvailable {
		flags := uintptr(0)
		label := "更新到 " + snapshot.updateRelease.Tag
		if a.installingUpdate.Load() {
			flags = 0x0001
			label = "正在更新…"
		}
		appendMenu(menu, flags, menuInstallUpdate, label)
	}
	appendMenu(menu, 0x00000800, 0, "")
	intervalMenu, _, _ := procCreatePopupMenu.Call()
	if intervalMenu != 0 {
		appendMenu(intervalMenu, 0, menuRefresh10, "10分钟")
		appendMenu(intervalMenu, 0, menuRefresh20, "20分钟")
		appendMenu(intervalMenu, 0, menuRefresh30, "30分钟")
		appendMenu(intervalMenu, 0, menuRefresh60, "1小时")
		appendMenu(menu, 0x00000010, intervalMenu, "刷新间隔")
		minutes := a.snapshot().settings.RefreshMinutes
		checkMenu(intervalMenu, menuRefresh10, minutes == 10)
		checkMenu(intervalMenu, menuRefresh20, minutes == 20)
		checkMenu(intervalMenu, menuRefresh30, minutes == 30)
		checkMenu(intervalMenu, menuRefresh60, minutes == 60)
	}
	appendMenu(menu, 0x00000800, 0, "")
	appendMenu(menu, 0, menuOpenPanel, "窗口打开")
	appendMenu(menu, 0, menuTopmost, "面板始终置顶")
	appendMenu(menu, 0, menuStartup, "登录 Windows 时启动")
	appendMenu(menu, 0x00000800, 0, "")
	appendMenu(menu, 0, menuExit, "退出")
	checkMenu(menu, menuTopmost, a.snapshot().settings.AlwaysOnTop)
	checkMenu(menu, menuStartup, a.state.startup)

	pt := point{}
	procGetCursorPos.Call(uintptr(unsafe.Pointer(&pt)))
	procSetForegroundWindow.Call(uintptr(a.mainWindow))
	selected, _, _ := procTrackPopupMenu.Call(
		menu,
		tpmRightButton|tpmReturnCommand,
		uintptr(pt.X), uintptr(pt.Y),
		0, uintptr(a.mainWindow), 0,
	)
	procPostMessage.Call(uintptr(a.mainWindow), wmNull, 0, 0)
	if selected != 0 {
		a.handleMenuCommand(uint16(selected))
	}
	a.menuOpen = false
	if uint16(selected) != menuOpenPanel && !a.snapshot().settings.AlwaysOnTop && a.panelVisible {
		a.hidePanel()
	}
}

func (a *monitorApp) handlePanelButton(button string) {
	switch button {
	case "theme":
		a.state.mu.Lock()
		switch normalizedThemeMode(a.state.settings.ThemeMode) {
		case themeSystem:
			a.state.settings.ThemeMode = themeLight
		case themeLight:
			a.state.settings.ThemeMode = themeDark
		default:
			a.state.settings.ThemeMode = themeSystem
		}
		settings := a.state.settings
		a.state.mu.Unlock()
		saveSettings(settings)
		procInvalidateRect.Call(uintptr(a.panelWindow), 0, 1)
	case "minimize":
		a.hidePanel()
	case "exit":
		a.shutdown()
	case "reset":
		a.resetQuota()
	case "topmost":
		a.toggleAlwaysOnTop()
	case "update":
		a.installUpdate()
	}
}

func (a *monitorApp) panelButtonAt(pt point) string {
	switch {
	case containsPoint(panelResetButtonRect(46, nominalCardHeight(a.snapshot().quota.Nominal)), pt) && a.snapshot().quota.ResetSupported:
		return "reset"
	case containsPoint(panelUpdateButtonRect(), pt) && a.snapshot().updateAvailable && !a.installingUpdate.Load() && !strings.HasPrefix(a.snapshot().updateStatus, "更新失败"):
		return "update"
	case containsPoint(panelThemeButtonRect(), pt):
		return "theme"
	case containsPoint(panelTopmostButtonRect(), pt):
		return "topmost"
	case containsPoint(panelMinimizeButtonRect(), pt):
		return "minimize"
	case containsPoint(panelExitButtonRect(), pt):
		return "exit"
	default:
		return ""
	}
}

func panelResetButtonRect(top, height int32) rect {
	sectionTop := top + 84
	sectionBottom := top + height - 10
	buttonTop := (sectionTop+sectionBottom)/2 - 17
	return rect{Left: panelWidth - 58, Top: buttonTop, Right: panelWidth - 24, Bottom: buttonTop + 34}
}

func panelThemeButtonRect() rect {
	return rect{Left: panelWidth - 204, Top: 6, Right: panelWidth - 112, Bottom: 36}
}

func panelUpdateButtonRect() rect {
	return rect{Left: 196, Top: 6, Right: panelWidth - 210, Bottom: 36}
}

func panelMinimizeButtonRect() rect {
	return rect{Left: panelWidth - 66, Top: 6, Right: panelWidth - 38, Bottom: 36}
}

func panelExitButtonRect() rect {
	return rect{Left: panelWidth - 30, Top: 6, Right: panelWidth - 8, Bottom: 36}
}

func panelTopmostButtonRect() rect {
	return rect{Left: panelWidth - 102, Top: 6, Right: panelWidth - 74, Bottom: 36}
}

func containsPoint(bounds rect, pt point) bool {
	return pt.X >= bounds.Left && pt.X < bounds.Right && pt.Y >= bounds.Top && pt.Y < bounds.Bottom
}

func pointFromLParam(lParam uintptr) point {
	return point{X: int32(int16(uint16(lParam))), Y: int32(int16(uint16(lParam >> 16)))}
}

func appendMenu(menu uintptr, flags, id uintptr, text string) {
	p, _ := syscall.UTF16PtrFromString(text)
	procAppendMenu.Call(menu, flags, id, uintptr(unsafe.Pointer(p)))
}

func checkMenu(menu uintptr, id uint16, checked bool) {
	flags := uintptr(0)
	if checked {
		flags = 0x0008
	}
	procCheckMenuItem.Call(menu, uintptr(id), 0x0000|flags)
}

func (a *monitorApp) handleMenuCommand(command uint16) {
	switch command {
	case menuRefresh:
		a.startFetch()
	case menuCheckUpdates:
		a.checkForUpdate(true)
	case menuInstallUpdate:
		a.installUpdate()
	case menuRefresh10:
		a.setRefreshInterval(10)
	case menuRefresh20:
		a.setRefreshInterval(20)
	case menuRefresh30:
		a.setRefreshInterval(30)
	case menuRefresh60:
		a.setRefreshInterval(60)
	case menuOpenPanel:
		a.showPanel()
	case menuTopmost:
		a.toggleAlwaysOnTop()
	case menuStartup:
		a.state.mu.RLock()
		enabled := !a.state.startup
		a.state.mu.RUnlock()
		if err := setStartup(enabled); err == nil {
			a.state.mu.Lock()
			a.state.startup = enabled
			a.state.mu.Unlock()
		} else {
			a.state.mu.Lock()
			a.state.status = "开机启动设置失败：" + shortError(err.Error())
			a.state.mu.Unlock()
			procPostMessage.Call(uintptr(a.mainWindow), wmStateChanged, 0, 0)
		}
	case menuExit:
		a.shutdown()
	}
}

func (a *monitorApp) toggleAlwaysOnTop() {
	a.state.mu.Lock()
	a.state.settings.AlwaysOnTop = !a.state.settings.AlwaysOnTop
	topmost := a.state.settings.AlwaysOnTop
	settings := a.state.settings
	a.state.mu.Unlock()
	saveSettings(settings)
	insertAfter := uintptr(hwndNotopmost)
	if topmost {
		insertAfter = hwndTopmost
	}
	procSetWindowPos.Call(uintptr(a.panelWindow), insertAfter, 0, 0, 0, 0, swpNoMove|swpNoSize|swpNoActivate)
	if a.panelVisible {
		procInvalidateRect.Call(uintptr(a.panelWindow), 0, 1)
	}
}

func (a *monitorApp) setRefreshInterval(minutes int) {
	if !validRefreshInterval(minutes) {
		return
	}
	a.state.mu.Lock()
	a.state.settings.RefreshMinutes = minutes
	a.state.status = "刷新间隔：" + refreshIntervalLabel(minutes)
	settings := a.state.settings
	a.state.mu.Unlock()
	saveSettings(settings)
	select {
	case <-a.intervalChange:
	default:
	}
	a.intervalChange <- time.Duration(minutes) * time.Minute
	procPostMessage.Call(uintptr(a.mainWindow), wmStateChanged, 0, 0)
}

func (a *monitorApp) resetQuota() {
	if a.resetPending.Load() {
		showMessageBox(a.panelWindow, "上一次重置结果仍在处理中，请稍候。", mbOK|mbIconInformation)
		return
	}
	snapshot := a.snapshot()
	if !snapshot.quota.ResetSupported {
		showMessageBox(a.panelWindow, "当前认证方式不支持此重置操作。", mbOK|mbIconInformation)
		return
	}
	if snapshot.quota.Nominal.Reset.AvailableCount < 1 {
		showMessageBox(a.panelWindow, "当前没有可用的重置次数，因此没有发送请求。", mbOK|mbIconInformation)
		return
	}
	confirmation := fmt.Sprintf(
		"即将为名义账户 %s 消耗 1 次可用重置额度。\n\n服务端会对该 API Key 对应名义前缀下的每个账户最多消耗 1 次；你已说明当前服务端配置为单账号。\n\n重置可能无法撤销，是否继续？",
		accountEmail(snapshot.quota.Nominal),
	)
	if snapshot.quota.DirectMode {
		confirmation = fmt.Sprintf(
			"即将使用当前 Codex 认证为账户 %s 消耗 1 次可用重置额度。\n\n该操作会直接请求插件的账户重置接口，可能无法撤销；请求不会自动重试。是否继续？",
			accountEmail(snapshot.quota.Nominal),
		)
	}
	if showMessageBox(a.panelWindow, confirmation, mbYesNo|mbIconWarning|mbDefaultButton2) != idYes {
		return
	}
	if !a.fetching.CompareAndSwap(false, true) {
		showMessageBox(a.panelWindow, "当前正在刷新额度或执行其他操作，请稍后再试。", mbOK|mbIconInformation)
		return
	}
	a.resetPending.Store(true)
	a.state.mu.Lock()
	a.state.status = "正在消耗重置额度…"
	a.state.mu.Unlock()
	procPostMessage.Call(uintptr(a.mainWindow), wmStateChanged, 0, 0)
	go func() {
		result, err := requestQuotaReset()
		if err != nil {
			detail := err.Error()
			if strings.Contains(detail, "读取 Codex 凭据") {
				detail = "读取本机 Codex 凭据失败，请检查 auth.json。"
			} else if strings.Contains(detail, "解析 Codex 凭据") {
				detail = "本机 auth.json 格式无效。"
			}
			result = "重置请求未能确认结果。\n\n" + detail + "\n\n请立即刷新剩余次数；程序不会自动重试。"
		}
		a.state.mu.Lock()
		a.state.resetResult = result
		if err != nil {
			a.state.status = "重置结果待核实"
		} else {
			a.state.status = "重置操作已返回"
		}
		a.state.mu.Unlock()
		procPostMessage.Call(uintptr(a.mainWindow), wmResetCompleted, 0, 0)
	}()
}

func (a *monitorApp) shutdown() {
	if a.trayAdded {
		nid := a.notifyData(a.trayIcon)
		procShellNotifyIcon.Call(nimDelete, uintptr(unsafe.Pointer(&nid)))
		a.trayAdded = false
	}
	if a.trayIcon != 0 {
		procDestroyIcon.Call(uintptr(a.trayIcon))
		a.trayIcon = 0
	}
	procCloseHandle.Call(uintptr(a.mutex))
	procPostQuitMessage.Call(0)
}

const (
	panelWidth       = 540
	actualCardHeight = 94
)

type panelColors struct {
	background uint32
	card       uint32
	border     uint32
	text       uint32
	muted      uint32
	track      uint32
	button     uint32
	buttonText uint32
}

func (a *monitorApp) paintPanel(hwnd syscall.Handle) {
	ps := paintStruct{}
	hdc, _, _ := procBeginPaint.Call(uintptr(hwnd), uintptr(unsafe.Pointer(&ps)))
	if hdc == 0 {
		return
	}
	defer procEndPaint.Call(uintptr(hwnd), uintptr(unsafe.Pointer(&ps)))
	s := a.snapshot()
	colors := colorsForTheme(s.settings.ThemeMode)

	background := createBrush(colors.background)
	defer procDeleteObject.Call(uintptr(background))
	full := rect{Right: panelWidth, Bottom: panelHeightFor(s.quota)}
	procFillRect.Call(hdc, uintptr(unsafe.Pointer(&full)), uintptr(background))
	procSetBkMode.Call(hdc, 1)
	fontFace, _ := syscall.UTF16PtrFromString("Microsoft YaHei UI")
	fontHeight := int32(-17)
	font, _, _ := procCreateFont.Call(
		uintptr(fontHeight), 0, 0, 0, 500,
		0, 0, 0, 1, 0, 0, 5, 0,
		uintptr(unsafe.Pointer(fontFace)),
	)
	if font != 0 {
		defer procDeleteObject.Call(font)
		oldFont, _, _ := procSelectObject.Call(hdc, font)
		defer procSelectObject.Call(hdc, oldFont)
	}
	themeLabel := "跟随系统"
	switch normalizedThemeMode(s.settings.ThemeMode) {
	case themeLight:
		themeLabel = "亮色"
	case themeDark:
		themeLabel = "暗色"
	}
	drawText(hdc, "Codex 额度监控", rect{Left: 18, Top: 6, Right: 190, Bottom: 36}, colors.text, dtSingleLine|dtVCenter)
	if s.updateAvailable && !strings.HasPrefix(s.updateStatus, "更新失败") {
		label := "更新到 " + s.updateRelease.Tag
		if a.installingUpdate.Load() {
			label = "正在更新…"
		}
		drawPanelButton(hdc, panelUpdateButtonRect(), label, colors)
	} else {
		headerStatus := s.updateStatus
		if headerStatus == "" {
			headerStatus = s.status
		}
		drawText(hdc, headerStatus, panelUpdateButtonRect(), colors.muted, dtSingleLine|dtVCenter|dtEndEllipsis)
	}
	drawPanelButton(hdc, panelThemeButtonRect(), themeLabel, colors)
	drawPinButton(hdc, panelTopmostButtonRect(), colors, s.settings.AlwaysOnTop)
	drawPanelButton(hdc, panelMinimizeButtonRect(), "—", colors)
	drawPanelButton(hdc, panelExitButtonRect(), "×", colors)

	nominalTop := int32(46)
	nominalHeight := nominalCardHeight(s.quota.Nominal)
	drawNominalAccountCard(hdc, colors, nominalTop, nominalHeight, s.quota.Nominal, s.quota.DirectMode, s.quota.SingleAccount, s.quota.ResetSupported)
	if s.quota.ActualAvailable {
		actualTop := nominalTop + nominalHeight + 10
		drawActualAccountCard(hdc, colors, actualTop, s.quota.Actual)
	}
}

func drawPanelButton(hdc uintptr, bounds rect, label string, colors panelColors) {
	brush := createBrush(colors.button)
	pen, _, _ := procCreatePen.Call(0, 1, uintptr(colors.border))
	oldBrush, _, _ := procSelectObject.Call(hdc, uintptr(brush))
	oldPen, _, _ := procSelectObject.Call(hdc, pen)
	procRoundRect.Call(hdc, uintptr(bounds.Left), uintptr(bounds.Top), uintptr(bounds.Right), uintptr(bounds.Bottom), 6, 6)
	procSelectObject.Call(hdc, oldPen)
	procSelectObject.Call(hdc, oldBrush)
	procDeleteObject.Call(pen)
	procDeleteObject.Call(uintptr(brush))
	drawText(hdc, label, bounds, colors.buttonText, dtSingleLine|dtVCenter|dtCenter)
}

func drawPinButton(hdc uintptr, bounds rect, colors panelColors, enabled bool) {
	background := colors.button
	iconColor := colors.buttonText
	if enabled {
		background = colorRef(63, 139, 214)
		iconColor = colorRef(255, 255, 255)
	}
	brush := createBrush(background)
	pen, _, _ := procCreatePen.Call(0, 1, uintptr(colors.border))
	oldBrush, _, _ := procSelectObject.Call(hdc, uintptr(brush))
	oldPen, _, _ := procSelectObject.Call(hdc, pen)
	procRoundRect.Call(hdc, uintptr(bounds.Left), uintptr(bounds.Top), uintptr(bounds.Right), uintptr(bounds.Bottom), 6, 6)
	procSelectObject.Call(hdc, oldPen)
	procSelectObject.Call(hdc, oldBrush)
	procDeleteObject.Call(pen)
	procDeleteObject.Call(uintptr(brush))
	drawText(hdc, "📌", bounds, iconColor, dtSingleLine|dtVCenter|dtCenter)
}

func drawNominalAccountCard(hdc uintptr, colors panelColors, top, height int32, account accountQuota, directMode, singleAccount, resetSupported bool) {
	drawCardBackground(hdc, colors, top, height)
	accountTitle := nominalAccountTitle(directMode, singleAccount)
	drawText(hdc, accountTitle+"  ·  "+accountEmail(account),
		rect{Left: 27, Top: top + 5, Right: panelWidth - 25, Bottom: top + 29}, colors.text, dtSingleLine|dtVCenter|dtEndEllipsis)
	drawMeter(hdc, "5小时", 27, top+38, account.Primary, colors)
	drawMeter(hdc, "周额度", 27, top+62, account.Secondary, colors)
	drawHorizontalDivider(hdc, 27, panelWidth-27, top+83, colors.border)
	drawText(hdc, fmt.Sprintf("重置次数  ·  %d 次", account.Reset.AvailableCount),
		rect{Left: 27, Top: top + 86, Right: panelWidth - 74, Bottom: top + 109}, colors.text, dtSingleLine|dtVCenter|dtEndEllipsis)
	if resetSupported {
		drawPanelButton(hdc, panelResetButtonRect(top, height), "↻", colors)
	}
	for i, line := range resetCreditLines(account.Reset, time.Now()) {
		rowTop := top + 112 + int32(i)*20
		drawText(hdc, line, rect{Left: 27, Top: rowTop, Right: panelWidth - 72, Bottom: rowTop + 19}, colors.muted, dtSingleLine|dtVCenter|dtEndEllipsis)
	}
}

func nominalAccountTitle(directMode, singleAccount bool) string {
	if singleAccount {
		return "账户"
	}
	if directMode {
		return "ChatGPT 账户"
	}
	return "名义账户"
}

func drawActualAccountCard(hdc uintptr, colors panelColors, top int32, account accountQuota) {
	drawCardBackground(hdc, colors, top, actualCardHeight)
	drawText(hdc, "实际账户  ·  "+accountEmail(account),
		rect{Left: 27, Top: top + 5, Right: panelWidth - 25, Bottom: top + 27}, colors.text, dtSingleLine|dtVCenter|dtEndEllipsis)
	drawMeter(hdc, "5小时", 27, top+39, account.Primary, colors)
	drawMeter(hdc, "周额度", 27, top+63, account.Secondary, colors)
}

func accountEmail(account accountQuota) string {
	if account.Email == "" {
		return "未知账户"
	}
	return account.Email
}

func drawCardBackground(hdc uintptr, colors panelColors, top, height int32) {
	brush := createBrush(colors.card)
	pen, _, _ := procCreatePen.Call(0, 1, uintptr(colors.border))
	oldBrush, _, _ := procSelectObject.Call(hdc, uintptr(brush))
	oldPen, _, _ := procSelectObject.Call(hdc, pen)
	procRoundRect.Call(hdc, 14, uintptr(top), panelWidth-14, uintptr(top+height), 9, 9)
	procSelectObject.Call(hdc, oldPen)
	procSelectObject.Call(hdc, oldBrush)
	procDeleteObject.Call(pen)
	procDeleteObject.Call(uintptr(brush))
}

func drawHorizontalDivider(hdc uintptr, left, right, y int32, color uint32) {
	pen, _, _ := procCreatePen.Call(0, 1, uintptr(color))
	oldPen, _, _ := procSelectObject.Call(hdc, pen)
	procMoveToEx.Call(hdc, uintptr(left), uintptr(y), 0)
	procLineTo.Call(hdc, uintptr(right), uintptr(y))
	procSelectObject.Call(hdc, oldPen)
	procDeleteObject.Call(pen)
}

func drawMeter(hdc uintptr, label string, left, top int32, bucket quotaBucket, colors panelColors) {
	drawText(hdc, label, rect{Left: left, Top: top - 4, Right: left + 54, Bottom: top + 16}, colors.muted, dtSingleLine|dtVCenter)
	trackBrush := createBrush(colors.track)
	trackPen, _, _ := procCreatePen.Call(0, 1, uintptr(colors.track))
	oldBrush, _, _ := procSelectObject.Call(hdc, uintptr(trackBrush))
	oldPen, _, _ := procSelectObject.Call(hdc, trackPen)
	barLeft, barRight := left+64, left+235
	procRoundRect.Call(hdc, uintptr(barLeft), uintptr(top), uintptr(barRight), uintptr(top+12), 6, 6)
	procSelectObject.Call(hdc, oldPen)
	procSelectObject.Call(hdc, oldBrush)
	procDeleteObject.Call(trackPen)
	procDeleteObject.Call(uintptr(trackBrush))

	if bucket.Known && bucket.Percent > 0 {
		width := int32(float64(barRight-barLeft) * float64(bucket.Percent) / 100.0)
		if width < 4 {
			width = 4
		}
		fill := quotaColor(bucket.Percent)
		fillBrush := createBrush(fill)
		fillPen, _, _ := procCreatePen.Call(0, 1, uintptr(fill))
		oldBrush, _, _ := procSelectObject.Call(hdc, uintptr(fillBrush))
		oldPen, _, _ := procSelectObject.Call(hdc, fillPen)
		procRoundRect.Call(hdc, uintptr(barLeft), uintptr(top), uintptr(barLeft+width), uintptr(top+12), 6, 6)
		procSelectObject.Call(hdc, oldPen)
		procSelectObject.Call(hdc, oldBrush)
		procDeleteObject.Call(fillPen)
		procDeleteObject.Call(uintptr(fillBrush))
	}
	drawText(hdc, bucketText(bucket),
		rect{Left: barRight + 8, Top: top - 4, Right: barRight + 64, Bottom: top + 16},
		colors.text, dtSingleLine|dtVCenter)
	drawText(hdc, quotaRemainingText(bucket, time.Now()),
		rect{Left: barRight + 72, Top: top - 4, Right: panelWidth - 24, Bottom: top + 16},
		colors.muted, dtSingleLine|dtVCenter|dtEndEllipsis)
}

func quotaColor(percent int) uint32 {
	if percent < 0 {
		percent = 0
	} else if percent > 100 {
		percent = 100
	}
	var from, to [3]float64
	var fraction float64
	if percent <= 50 {
		from = [3]float64{232, 68, 68}
		to = [3]float64{70, 145, 255}
		fraction = float64(percent) / 50
	} else {
		from = [3]float64{70, 145, 255}
		to = [3]float64{52, 190, 116}
		fraction = float64(percent-50) / 50
	}
	return colorRef(
		byte(math.Round(from[0]+(to[0]-from[0])*fraction)),
		byte(math.Round(from[1]+(to[1]-from[1])*fraction)),
		byte(math.Round(from[2]+(to[2]-from[2])*fraction)),
	)
}

func drawText(hdc uintptr, text string, bounds rect, color uint32, flags uintptr) {
	wide, _ := syscall.UTF16FromString(text)
	procSetTextColor.Call(hdc, uintptr(color))
	procDrawText.Call(hdc, uintptr(unsafe.Pointer(&wide[0])), ^uintptr(0), uintptr(unsafe.Pointer(&bounds)), flags)
}

func createBrush(color uint32) syscall.Handle {
	r, _, _ := procCreateSolidBrush.Call(uintptr(color))
	return syscall.Handle(r)
}

func colorRef(r, g, b byte) uint32 {
	return uint32(r) | uint32(g)<<8 | uint32(b)<<16
}

func normalizedThemeMode(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case themeLight:
		return themeLight
	case themeDark:
		return themeDark
	default:
		return themeSystem
	}
}

func colorsForTheme(mode string) panelColors {
	dark := normalizedThemeMode(mode) == themeDark ||
		(normalizedThemeMode(mode) == themeSystem && !systemPrefersLightTheme())
	if dark {
		return panelColors{
			background: colorRef(19, 24, 34),
			card:       colorRef(29, 37, 50),
			border:     colorRef(49, 62, 79),
			text:       colorRef(235, 241, 248),
			muted:      colorRef(155, 168, 184),
			track:      colorRef(54, 65, 81),
			button:     colorRef(36, 45, 60),
			buttonText: colorRef(220, 229, 239),
		}
	}
	return panelColors{
		background: colorRef(243, 246, 250),
		card:       colorRef(255, 255, 255),
		border:     colorRef(216, 223, 232),
		text:       colorRef(35, 45, 58),
		muted:      colorRef(104, 116, 132),
		track:      colorRef(226, 231, 238),
		button:     colorRef(235, 240, 246),
		buttonText: colorRef(53, 66, 83),
	}
}

func systemPrefersLightTheme() bool {
	keyPath, _ := syscall.UTF16PtrFromString("Software\\Microsoft\\Windows\\CurrentVersion\\Themes\\Personalize")
	valueName, _ := syscall.UTF16PtrFromString("AppsUseLightTheme")
	var key uintptr
	r, _, _ := procRegOpenKeyEx.Call(hkeyCurrentUser, uintptr(unsafe.Pointer(keyPath)), 0, keyQueryValue, uintptr(unsafe.Pointer(&key)))
	if r != 0 {
		return true
	}
	defer procRegCloseKey.Call(key)
	var value, valueType, size uint32
	size = uint32(unsafe.Sizeof(value))
	r, _, _ = procRegQueryValueEx.Call(key, uintptr(unsafe.Pointer(valueName)), 0,
		uintptr(unsafe.Pointer(&valueType)), uintptr(unsafe.Pointer(&value)), uintptr(unsafe.Pointer(&size)))
	if r != 0 || valueType != regDword {
		return true
	}
	return value != 0
}

func nominalCardHeight(account accountQuota) int32 {
	rows := len(resetCreditLines(account.Reset, time.Now()))
	return int32(122 + rows*20)
}

func panelHeightFor(quota quotaSnapshot) int32 {
	height := int32(46) + nominalCardHeight(quota.Nominal) + 14
	if quota.ActualAvailable {
		height += 10 + actualCardHeight
	}
	return height
}

func resetCreditLines(info resetCreditInfo, now time.Time) []string {
	type expiry struct {
		at time.Time
	}
	valid := make([]expiry, 0, len(info.ExpiresAt))
	invalid := make([]string, 0)
	for _, raw := range info.ExpiresAt {
		value := strings.TrimSpace(raw)
		parsed, err := time.Parse(time.RFC3339Nano, value)
		if err != nil {
			invalid = append(invalid, "到期 "+value+" · 时间格式异常")
			continue
		}
		valid = append(valid, expiry{at: parsed})
	}
	sort.Slice(valid, func(i, j int) bool { return valid[i].at.Before(valid[j].at) })
	lines := make([]string, 0, len(valid)+len(invalid)+2)
	for _, item := range valid {
		lines = append(lines, "到期 "+item.at.Local().Format("2006-01-02 15:04")+"  ·  剩余 "+remainingTime(item.at.Sub(now)))
	}
	lines = append(lines, invalid...)
	if info.WithoutExpiry > 0 {
		lines = append(lines, fmt.Sprintf("无固定到期时间：%d 次", info.WithoutExpiry))
	}
	if info.AvailableCount == 0 && len(lines) == 0 {
		return []string{"当前没有可用重置次数"}
	}
	if !info.ExpiryDetailsAvailable && len(valid) == 0 {
		lines = append(lines, "有效期明细暂不可用")
	} else {
		missing := info.AvailableCount - len(info.ExpiresAt) - info.WithoutExpiry
		if missing > 0 {
			lines = append(lines, fmt.Sprintf("还有 %d 次的到期时间未返回", missing))
		} else if !info.ExpiryDetailsComplete {
			lines = append(lines, "有效期明细可能不完整")
		}
	}
	if len(lines) == 0 {
		return []string{"暂无到期明细"}
	}
	return lines
}

func remainingTime(remaining time.Duration) string {
	if remaining <= 0 {
		return "已到期"
	}
	if remaining < time.Hour {
		minutes := int(remaining.Minutes())
		if minutes < 1 {
			minutes = 1
		}
		return fmt.Sprintf("%d 分钟", minutes)
	}
	hours := int(remaining.Hours())
	if hours >= 24 {
		return fmt.Sprintf("%d 天 %d 小时", hours/24, hours%24)
	}
	return fmt.Sprintf("%d 小时", hours)
}

func createTrayIcon(actual accountQuota) syscall.Handle {
	const size = 32
	dc, _, _ := procGetDC.Call(0)
	info := bitmapInfo{Header: bitmapInfoHeader{
		Size: uint32(unsafe.Sizeof(bitmapInfoHeader{})), Width: size, Height: -size,
		Planes: 1, BitCount: 32, Compression: biRGB, SizeImage: size * size * 4,
	}}
	var bits uintptr
	bitmap, _, _ := procCreateDIBSection.Call(dc, uintptr(unsafe.Pointer(&info)), dibRGBColors, uintptr(unsafe.Pointer(&bits)), 0, 0)
	procReleaseDC.Call(0, dc)
	if bitmap == 0 || bits == 0 {
		return 0
	}
	pixels := unsafe.Slice((*byte)(unsafe.Pointer(bits)), size*size*4)
	drawIconMeter(pixels, size, 4, actual.Primary)
	drawIconMeter(pixels, size, 18, actual.Secondary)

	maskBits := make([]byte, size*4)
	mask, _, _ := procCreateBitmap.Call(size, size, 1, 1, uintptr(unsafe.Pointer(&maskBits[0])))
	ii := iconInfo{Icon: 1, Mask: syscall.Handle(mask), Color: syscall.Handle(bitmap)}
	icon, _, _ := procCreateIconIndirect.Call(uintptr(unsafe.Pointer(&ii)))
	procDeleteObject.Call(mask)
	procDeleteObject.Call(bitmap)
	return syscall.Handle(icon)
}

func drawIconMeter(pixels []byte, size int, y int, bucket quotaBucket) {
	track := colorRef(48, 59, 74)
	fillRoundedPixels(pixels, size, 2, y, 28, 10, 3, track)
	fillRoundedPixels(pixels, size, 4, y+2, 24, 6, 2, colorRef(21, 27, 37))
	if !bucket.Known {
		return
	}
	width := int(math.Round(float64(24) * float64(bucket.Percent) / 100.0))
	if width > 0 {
		fillRoundedPixels(pixels, size, 4, y+2, width, 6, 2, quotaColor(bucket.Percent))
	}
}

func fillRoundedPixels(pixels []byte, size, left, top, width, height, radius int, color uint32) {
	r := float64(radius)
	for y := top; y < top+height; y++ {
		for x := left; x < left+width; x++ {
			cx, cy := float64(x), float64(y)
			if x < left+radius {
				cx = float64(left + radius)
			} else if x >= left+width-radius {
				cx = float64(left + width - radius - 1)
			}
			if y < top+radius {
				cy = float64(top + radius)
			} else if y >= top+height-radius {
				cy = float64(top + height - radius - 1)
			}
			dx, dy := float64(x)-cx, float64(y)-cy
			if dx*dx+dy*dy > r*r {
				continue
			}
			i := (y*size + x) * 4
			pixels[i] = byte(color >> 16)
			pixels[i+1] = byte(color >> 8)
			pixels[i+2] = byte(color)
			pixels[i+3] = 255
		}
	}
}

type responseEnvelope struct {
	Nominal json.RawMessage "json:\"nominal_accounts\""
	Actual  json.RawMessage "json:\"actual_accounts\""
}

type directResponseEnvelope struct {
	Accounts map[string]json.RawMessage `json:"accounts"`
}

type quotaResetResponse struct {
	Success         bool                               `json:"success"`
	Partial         bool                               `json:"partial"`
	NominalAccounts map[string]quotaResetAccountResult `json:"nominal_accounts"`
	Errors          []string                           `json:"errors"`
}

type quotaDirectResetResponse struct {
	Success  bool                               `json:"success"`
	Partial  bool                               `json:"partial"`
	Accounts map[string]quotaResetAccountResult `json:"accounts"`
	Errors   []string                           `json:"errors"`
}

type quotaResetAccountResult struct {
	Success bool   `json:"success"`
	Message string `json:"message"`
}

type accountDetails struct {
	Groups       []quotaGroup    "json:\"groups\""
	ResetCredits resetCreditInfo "json:\"reset_credits\""
}

type quotaGroup struct {
	DisplayName string           "json:\"displayName\""
	Buckets     []quotaBucketRaw "json:\"buckets\""
}

type quotaBucketRaw struct {
	Window            string   "json:\"window\""
	RemainingFraction *float64 "json:\"remainingFraction\""
	ResetTime         string   "json:\"resetTime\""
	ResetTimeSnake    string   "json:\"reset_time\""
}

func fetchQuota() (quotaSnapshot, error) {
	credentials, err := readQuotaCredentials()
	if err != nil {
		return quotaSnapshot{}, err
	}
	client := &http.Client{Timeout: 12 * time.Second}
	return fetchQuotaWithRetry(func() (quotaSnapshot, error) {
		return fetchQuotaUsingAuth(client, credentials, apiURL, directAPIURL)
	}, time.Sleep)
}

func fetchQuotaWithRetry(request func() (quotaSnapshot, error), sleep func(time.Duration)) (quotaSnapshot, error) {
	var lastErr error
	for attempt := 0; attempt <= quotaRequestRetries; attempt++ {
		result, err := request()
		if err == nil {
			return result, nil
		}
		lastErr = err
		if attempt == quotaRequestRetries {
			break
		}
		delay := time.Second << attempt
		if delay > 8*time.Second {
			delay = 8 * time.Second
		}
		sleep(delay)
	}
	return quotaSnapshot{}, fmt.Errorf("额度查询连续失败（共尝试 %d 次）：%w", quotaRequestRetries+1, lastErr)
}

func fetchQuotaOnce(client *http.Client, apiKey string) (quotaSnapshot, error) {
	return fetchQuotaAt(client, apiURL, apiKey)
}

func fetchQuotaUsingAuth(client *http.Client, credentials quotaCredentials, apiKeyEndpoint, chatGPTEndpoint string) (quotaSnapshot, error) {
	switch credentials.Mode {
	case authModeAPIKey:
		return fetchQuotaAt(client, apiKeyEndpoint, credentials.APIKey)
	case authModeChatGPT:
		return fetchDirectQuotaAt(client, chatGPTEndpoint, credentials.AccessToken, credentials.AccountID)
	default:
		return quotaSnapshot{}, errors.New("auth.json 中 auth_mode 不受支持")
	}
}

func fetchQuotaAt(client *http.Client, endpoint, apiKey string) (quotaSnapshot, error) {
	var result quotaSnapshot
	req, err := http.NewRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		return result, err
	}
	req.Header.Set("User-Agent", "QuotaTrayMonitor/1.0")
	req.Header.Set("Authorization", "Bearer "+apiKey)
	resp, err := client.Do(req)
	if err != nil {
		return result, fmt.Errorf("额度接口请求失败：%w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return result, fmt.Errorf("额度接口返回 HTTP %d", resp.StatusCode)
	}
	var envelope responseEnvelope
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		return result, fmt.Errorf("解析额度响应失败：%w", err)
	}
	return parseQuotaEnvelope(envelope), nil
}

func fetchDirectQuotaAt(client *http.Client, endpoint, accessToken, accountID string) (quotaSnapshot, error) {
	req, err := http.NewRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		return quotaSnapshot{}, err
	}
	setDirectQuotaHeaders(req, accessToken, accountID)
	resp, err := client.Do(req)
	if err != nil {
		return quotaSnapshot{}, fmt.Errorf("直接额度接口请求失败：%w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return quotaSnapshot{}, fmt.Errorf("直接额度接口返回 HTTP %d", resp.StatusCode)
	}
	var envelope directResponseEnvelope
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		return quotaSnapshot{}, fmt.Errorf("解析直接额度响应失败：%w", err)
	}
	return parseDirectQuotaEnvelope(envelope)
}

func parseQuotaEnvelope(envelope responseEnvelope) quotaSnapshot {
	nominalCount := countAccounts(envelope.Nominal)
	actualCount := countAccounts(envelope.Actual)
	return quotaSnapshot{
		Nominal:          parseAccount(envelope.Nominal),
		Actual:           parseAccount(envelope.Actual),
		NominalAvailable: nominalCount > 0,
		ActualAvailable:  actualCount > 0,
		SingleAccount:    nominalCount+actualCount == 1,
		ResetSupported:   true,
	}
}

func parseDirectQuotaEnvelope(envelope directResponseEnvelope) (quotaSnapshot, error) {
	if len(envelope.Accounts) == 0 {
		return quotaSnapshot{}, errors.New("直接额度响应中没有账户数据")
	}
	accountsJSON, err := json.Marshal(envelope.Accounts)
	if err != nil {
		return quotaSnapshot{}, errors.New("解析直接额度账户数据失败")
	}
	return quotaSnapshot{
		Nominal:          parseAccount(accountsJSON),
		NominalAvailable: true,
		SingleAccount:    len(envelope.Accounts) == 1,
		DirectMode:       true,
		ResetSupported:   true,
	}, nil
}

func setDirectQuotaHeaders(req *http.Request, accessToken, accountID string) {
	req.Header.Set("User-Agent", "QuotaTrayMonitor/1.0")
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("ChatGPT-Account-ID", accountID)
}

func hasActualAccounts(raw json.RawMessage) bool {
	return countAccounts(raw) > 0
}

func countAccounts(raw json.RawMessage) int {
	value := bytes.TrimSpace(raw)
	if len(value) == 0 || value[0] != '{' {
		return 0
	}
	var accounts map[string]json.RawMessage
	if err := json.Unmarshal(value, &accounts); err != nil {
		return 0
	}
	return len(accounts)
}

type quotaCredentials struct {
	Mode        string
	APIKey      string
	AccessToken string
	AccountID   string
}

func readQuotaCredentials() (quotaCredentials, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return quotaCredentials{}, err
	}
	return readQuotaCredentialsFromPath(filepath.Join(home, authRelPath))
}

func readQuotaCredentialsFromPath(path string) (quotaCredentials, error) {
	authBytes, err := os.ReadFile(path)
	if err != nil {
		return quotaCredentials{}, fmt.Errorf("读取 Codex 凭据失败：%w", err)
	}
	credentials, err := parseQuotaCredentials(authBytes)
	if err != nil {
		return quotaCredentials{}, fmt.Errorf("解析 Codex 凭据失败：%w", err)
	}
	return credentials, nil
}

func parseQuotaCredentials(authBytes []byte) (quotaCredentials, error) {
	var auth struct {
		AuthMode string          `json:"auth_mode"`
		APIKey   json.RawMessage `json:"OPENAI_API_KEY"`
		Tokens   struct {
			AccessToken string `json:"access_token"`
			AccountID   string `json:"account_id"`
		} `json:"tokens"`
	}
	if err := json.Unmarshal(authBytes, &auth); err != nil {
		return quotaCredentials{}, err
	}
	mode := strings.ToLower(strings.TrimSpace(auth.AuthMode))
	switch mode {
	case "", authModeAPIKey:
		var apiKey string
		if len(auth.APIKey) == 0 || string(bytes.TrimSpace(auth.APIKey)) == "null" || json.Unmarshal(auth.APIKey, &apiKey) != nil || strings.TrimSpace(apiKey) == "" {
			return quotaCredentials{}, errors.New("auth.json 中未找到有效的 OPENAI_API_KEY")
		}
		return quotaCredentials{Mode: authModeAPIKey, APIKey: apiKey}, nil
	case authModeChatGPT:
		accessToken := strings.TrimSpace(auth.Tokens.AccessToken)
		accountID := strings.TrimSpace(auth.Tokens.AccountID)
		if accessToken == "" || accountID == "" {
			return quotaCredentials{}, errors.New("auth.json 中 ChatGPT access_token 或 account_id 缺失")
		}
		return quotaCredentials{Mode: authModeChatGPT, AccessToken: accessToken, AccountID: accountID}, nil
	default:
		return quotaCredentials{}, errors.New("auth.json 中 auth_mode 不受支持")
	}
}

func readDownstreamAPIKey() (string, error) {
	credentials, err := readQuotaCredentials()
	if err != nil {
		return "", err
	}
	if credentials.Mode != authModeAPIKey {
		return "", errors.New("当前 auth_mode 不支持 CPA API Key 重置接口")
	}
	return credentials.APIKey, nil
}

func requestQuotaReset() (string, error) {
	credentials, err := readQuotaCredentials()
	if err != nil {
		return "", err
	}
	defaultTransport, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return "", errors.New("无法创建单次重置请求")
	}
	transport := defaultTransport.Clone()
	transport.DisableKeepAlives = true
	transport.TLSNextProto = make(map[string]func(string, *tls.Conn) http.RoundTripper)
	defer transport.CloseIdleConnections()
	client := &http.Client{
		Timeout:   12 * time.Second,
		Transport: transport,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	return requestQuotaResetWithAuth(client, credentials, resetAPIURL, directResetAPIURL)
}

func requestQuotaResetWithAuth(client *http.Client, credentials quotaCredentials, apiKeyEndpoint, chatGPTEndpoint string) (string, error) {
	endpoint := apiKeyEndpoint
	directMode := false
	var req *http.Request
	var err error
	switch credentials.Mode {
	case authModeAPIKey:
		req, err = http.NewRequest(http.MethodGet, endpoint, nil)
		if err == nil {
			req.Header.Set("User-Agent", "QuotaTrayMonitor/1.0")
			req.Header.Set("Authorization", "Bearer "+credentials.APIKey)
		}
	case authModeChatGPT:
		endpoint = chatGPTEndpoint
		directMode = true
		req, err = http.NewRequest(http.MethodGet, endpoint, nil)
		if err == nil {
			setDirectQuotaHeaders(req, credentials.AccessToken, credentials.AccountID)
		}
	default:
		return "", errors.New("auth.json 中 auth_mode 不受支持")
	}
	if err != nil {
		return "", err
	}
	if client == nil {
		return "", errors.New("无法创建单次重置请求")
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", errors.New("网络连接失败，无法判断服务端是否已消费额度")
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("服务端返回 HTTP %d，无法确认重置结果", resp.StatusCode)
	}
	if directMode {
		var result quotaDirectResetResponse
		if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&result); err != nil {
			return "", errors.New("服务端已响应，但结果无法解析；无法确认是否已消费额度")
		}
		return formatQuotaResetResponse(quotaResetResponse{
			Success:         result.Success,
			Partial:         result.Partial,
			NominalAccounts: result.Accounts,
			Errors:          result.Errors,
		}), nil
	}
	var result quotaResetResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&result); err != nil {
		return "", errors.New("服务端已响应，但结果无法解析；无法确认是否已消费额度")
	}
	return formatQuotaResetResponse(result), nil
}

func formatQuotaResetResponse(result quotaResetResponse) string {
	lines := []string{"重置结果："}
	switch {
	case result.Success:
		lines = append(lines, "全部账户重置成功。")
	case result.Partial:
		lines = append(lines, "部分账户重置成功，其余账户未成功。")
	default:
		lines = append(lines, "服务端未报告重置成功。")
	}
	accounts := make([]string, 0, len(result.NominalAccounts))
	for email := range result.NominalAccounts {
		accounts = append(accounts, email)
	}
	sort.Strings(accounts)
	for _, email := range accounts {
		account := result.NominalAccounts[email]
		status := "失败"
		if account.Success {
			status = "成功"
		}
		line := email + "：" + status
		if detail := localizeResetMessage(account.Message); detail != "" {
			line += "（" + detail + "）"
		}
		lines = append(lines, line)
	}
	if len(result.NominalAccounts) == 0 {
		lines = append(lines, "服务端没有返回逐账户结果。")
	}
	if len(result.Errors) > 0 {
		lines = append(lines, fmt.Sprintf("另有 %d 条服务端错误信息。", len(result.Errors)))
	}
	return strings.Join(lines, "\n")
}

func localizeResetMessage(message string) string {
	message = strings.TrimSpace(message)
	if message == "" {
		return ""
	}
	switch message {
	case "Codex quota reset credit consumed":
		return "已消耗 1 次重置额度"
	case "Codex credentials are unavailable":
		return "服务端无法读取该账户凭据"
	case "unable to check Codex reset-credit availability":
		return "无法检查重置额度是否可用"
	case "invalid Codex reset-credit response":
		return "服务端收到的额度数据无效"
	case "no Codex quota reset credits are available":
		return "没有可用的重置额度"
	case "no selectable Codex reset-credit details are available":
		return "没有可供消费的重置额度明细"
	case "invalid Codex base URL":
		return "账户服务地址无效"
	case "could not create a reset request identifier":
		return "无法创建重置请求编号"
	case "Codex reset-credit consumption failed; the final account state may be uncertain":
		return "消费请求失败，最终账户状态可能不确定"
	case "Codex did not confirm reset-credit consumption":
		return "服务端未确认重置额度已消费"
	default:
		return "服务端返回了其他状态，请刷新额度确认"
	}
}

func parseAccount(raw json.RawMessage) accountQuota {
	account := accountQuota{Email: "未知"}
	if len(raw) == 0 || string(raw) == "null" || string(raw) == "{}" {
		return account
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if _, err := decoder.Token(); err != nil {
		return account
	}
	if !decoder.More() {
		return account
	}
	keyToken, err := decoder.Token()
	if err != nil {
		return account
	}
	email, _ := keyToken.(string)
	account.Email = email
	var details accountDetails
	if err := decoder.Decode(&details); err != nil {
		return account
	}
	account.Reset = details.ResetCredits
	for _, group := range details.Groups {
		if group.DisplayName == "Codex" || !account.Primary.Known {
			for _, bucket := range group.Buckets {
				if bucket.RemainingFraction == nil {
					continue
				}
				percent := int(math.Round(*bucket.RemainingFraction * 100))
				if percent < 0 {
					percent = 0
				}
				if percent > 100 {
					percent = 100
				}
				resetAt := bucket.ResetTime
				if resetAt == "" {
					resetAt = bucket.ResetTimeSnake
				}
				quota := quotaBucket{Percent: percent, Known: true, ResetAt: parseQuotaResetTime(resetAt)}
				switch bucket.Window {
				case "primary":
					account.Primary = quota
				case "secondary":
					account.Secondary = quota
				}
			}
		}
	}
	return account
}

func bucketText(b quotaBucket) string {
	if !b.Known {
		return "--"
	}
	return fmt.Sprintf("%d%%", b.Percent)
}

func parseQuotaResetTime(raw string) time.Time {
	parsed, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(raw))
	if err != nil {
		return time.Time{}
	}
	return parsed
}

func quotaRemainingText(bucket quotaBucket, now time.Time) string {
	if bucket.ResetAt.IsZero() {
		return "剩余时间：--"
	}
	remaining := bucket.ResetAt.Sub(now)
	if remaining <= 0 {
		return "剩余时间：已到期"
	}
	if remaining < time.Hour {
		minutes := int(remaining.Minutes())
		if minutes < 1 {
			minutes = 1
		}
		return fmt.Sprintf("剩余时间：%d分", minutes)
	}
	hours := int(remaining.Hours())
	if hours >= 24 {
		return fmt.Sprintf("剩余时间：%d天%d小时", hours/24, hours%24)
	}
	minutes := int(remaining.Minutes()) % 60
	return fmt.Sprintf("剩余时间：%d小时%d分", hours, minutes)
}

func shortError(s string) string {
	s = strings.ReplaceAll(s, "\r", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	runes := []rune(s)
	if len(runes) > 46 {
		return string(runes[:43]) + "…"
	}
	return s
}

func configPath() string {
	base, err := os.UserConfigDir()
	if err != nil {
		base = os.TempDir()
	}
	return filepath.Join(base, "CodexQuotaTrayMonitor", "settings.json")
}

func loadSettings() appSettings {
	var settings appSettings
	data, err := os.ReadFile(configPath())
	if err == nil {
		_ = json.Unmarshal(data, &settings)
	}
	if !validRefreshInterval(settings.RefreshMinutes) {
		settings.RefreshMinutes = 10
	}
	settings.ThemeMode = normalizedThemeMode(settings.ThemeMode)
	return settings
}

func validRefreshInterval(minutes int) bool {
	return minutes == 10 || minutes == 20 || minutes == 30 || minutes == 60
}

func refreshIntervalLabel(minutes int) string {
	if minutes == 60 {
		return "1小时"
	}
	return fmt.Sprintf("%d分钟", minutes)
}

func saveSettings(settings appSettings) {
	path := configPath()
	if os.MkdirAll(filepath.Dir(path), 0700) != nil {
		return
	}
	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return
	}
	tmp := path + ".tmp"
	if os.WriteFile(tmp, data, 0600) == nil {
		_ = os.Rename(tmp, path)
	}
}

func startupEnabled() bool {
	keyPath, _ := syscall.UTF16PtrFromString("Software\\Microsoft\\Windows\\CurrentVersion\\Run")
	valueName, _ := syscall.UTF16PtrFromString("CodexQuotaTrayMonitor")
	var key uintptr
	r, _, _ := procRegOpenKeyEx.Call(hkeyCurrentUser, uintptr(unsafe.Pointer(keyPath)), 0, keyQueryValue, uintptr(unsafe.Pointer(&key)))
	if r != 0 {
		return false
	}
	defer procRegCloseKey.Call(key)
	var size uint32
	r, _, _ = procRegQueryValueEx.Call(key, uintptr(unsafe.Pointer(valueName)), 0, 0, 0, uintptr(unsafe.Pointer(&size)))
	return r == 0
}

func setStartup(enabled bool) error {
	keyPath, _ := syscall.UTF16PtrFromString("Software\\Microsoft\\Windows\\CurrentVersion\\Run")
	valueName, _ := syscall.UTF16PtrFromString("CodexQuotaTrayMonitor")
	var key uintptr
	r, _, _ := procRegCreateKeyEx.Call(
		hkeyCurrentUser, uintptr(unsafe.Pointer(keyPath)), 0, 0, 0,
		keySetValue, 0, uintptr(unsafe.Pointer(&key)), 0,
	)
	if r != 0 {
		return fmt.Errorf("打开启动注册表项失败（代码 %d）", r)
	}
	defer procRegCloseKey.Call(key)
	if !enabled {
		r, _, _ = procRegDeleteValue.Call(key, uintptr(unsafe.Pointer(valueName)))
		if r != 0 && r != errorFileNotFound {
			return fmt.Errorf("删除启动项失败（代码 %d）", r)
		}
		return nil
	}
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	command, _ := syscall.UTF16FromString("\"" + executable + "\"")
	r, _, _ = procRegSetValueEx.Call(
		key, uintptr(unsafe.Pointer(valueName)), 0, regSZ,
		uintptr(unsafe.Pointer(&command[0])), uintptr(uint32(len(command)*2)),
	)
	if r != 0 {
		return fmt.Errorf("写入启动项失败（代码 %d）", r)
	}
	return nil
}

func winError(operation string) error {
	err := syscall.GetLastError()
	if err == nil {
		return fmt.Errorf("%s 失败", operation)
	}
	return fmt.Errorf("%s 失败：%w", operation, err)
}

func showError(message string) {
	showMessageBox(0, message, 0x10)
}

func showMessageBox(owner syscall.Handle, message string, flags uintptr) int32 {
	text, _ := syscall.UTF16PtrFromString(message)
	title, _ := syscall.UTF16PtrFromString(windowName)
	result, _, _ := procMessageBox.Call(uintptr(owner), uintptr(unsafe.Pointer(text)), uintptr(unsafe.Pointer(title)), flags)
	return int32(result)
}

func init() {
	// Windows build intentionally uses only the Go standard library and Win32 APIs.
}

var procInvalidateRect = user32.NewProc("InvalidateRect")
