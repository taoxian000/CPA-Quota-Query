//go:build windows

package main

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode/utf16"
	"unsafe"
)

const (
	latestReleaseURL      = "https://api.github.com/repos/taoxian000/CPA-Quota-Query/releases/latest"
	legacyUpdateAssetName = "quota-monitor-windows-amd64.exe"
	maxUpdateBytes        = 64 << 20
	updateCheckEvery      = 24 * time.Hour
)

var (
	appVersion       = "v1.2"
	procShellExecute = shell32.NewProc("ShellExecuteW")
	errNoRelease     = errors.New("尚无正式 Release")
)

type releaseAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
	Digest             string `json:"digest"`
	Size               int64  `json:"size"`
}

type githubRelease struct {
	TagName    string         `json:"tag_name"`
	Prerelease bool           `json:"prerelease"`
	Draft      bool           `json:"draft"`
	Assets     []releaseAsset `json:"assets"`
}

type updateRelease struct {
	Tag    string
	URL    string
	Digest string
	Size   int64
}

func (a *monitorApp) updateCheckLoop() {
	a.checkForUpdate(false)
	ticker := time.NewTicker(updateCheckEvery)
	defer ticker.Stop()
	for range ticker.C {
		a.checkForUpdate(false)
	}
}

func (a *monitorApp) checkForUpdate(manual bool) {
	if !a.checkingUpdate.CompareAndSwap(false, true) {
		return
	}
	a.state.mu.Lock()
	a.state.updateChecking = true
	a.state.updateStatus = "正在检查更新…"
	a.state.mu.Unlock()
	procPostMessage.Call(uintptr(a.mainWindow), wmStateChanged, 0, 0)

	go func() {
		defer a.checkingUpdate.Store(false)
		client := &http.Client{
			Timeout: 20 * time.Second,
			CheckRedirect: func(request *http.Request, via []*http.Request) error {
				if len(via) >= 5 {
					return errors.New("更新检查重定向次数过多")
				}
				if len(via) > 0 && request.URL.Scheme != via[len(via)-1].URL.Scheme {
					return errors.New("拒绝不安全的更新检查重定向")
				}
				return nil
			},
		}
		release, err := fetchLatestRelease(client, latestReleaseURL)
		a.state.mu.Lock()
		a.state.updateChecking = false
		if err != nil {
			if errors.Is(err, errNoRelease) {
				a.state.updateStatus = ""
			} else {
				a.state.updateStatus = "检查更新失败：" + shortError(err.Error())
			}
		} else {
			comparison, compareErr := compareVersions(release.Tag, appVersion)
			if compareErr != nil {
				a.state.updateStatus = "检查更新失败：" + shortError(compareErr.Error())
			} else {
				a.state.updateAvailable = comparison > 0
				a.state.updateStatus = ""
				if a.state.updateAvailable {
					a.state.updateRelease = release
				} else {
					a.state.updateRelease = updateRelease{}
					if manual {
						a.state.updateStatus = "当前已是最新版本"
					}
				}
			}
		}
		a.state.mu.Unlock()
		procPostMessage.Call(uintptr(a.mainWindow), wmStateChanged, 0, 0)
	}()
}

func fetchLatestRelease(client *http.Client, endpoint string) (updateRelease, error) {
	request, err := http.NewRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		return updateRelease{}, err
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("User-Agent", "CPA-Quota-Query/"+appVersion)
	response, err := client.Do(request)
	if err != nil {
		return updateRelease{}, fmt.Errorf("GitHub 请求失败：%w", err)
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotFound {
		return updateRelease{}, errNoRelease
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return updateRelease{}, fmt.Errorf("GitHub 返回 HTTP %d", response.StatusCode)
	}
	var release githubRelease
	if err := json.NewDecoder(io.LimitReader(response.Body, 2<<20)).Decode(&release); err != nil {
		return updateRelease{}, fmt.Errorf("解析 GitHub Release 失败：%w", err)
	}
	return selectReleaseAsset(release)
}

func selectReleaseAsset(release githubRelease) (updateRelease, error) {
	if release.Draft || release.Prerelease {
		return updateRelease{}, errors.New("最新 Release 不是正式版本")
	}
	if _, err := parseVersion(release.TagName); err != nil {
		return updateRelease{}, fmt.Errorf("Release 版本号无效：%w", err)
	}
	versionedName := versionedUpdateAssetName(release.TagName)
	var asset *releaseAsset
	for i := range release.Assets {
		if release.Assets[i].Name == versionedName {
			if asset != nil {
				return updateRelease{}, errors.New("Release 中存在重复的 Windows 程序附件")
			}
			asset = &release.Assets[i]
		}
	}
	if asset == nil {
		for i := range release.Assets {
			if release.Assets[i].Name == legacyUpdateAssetName {
				if asset != nil {
					return updateRelease{}, errors.New("Release 中存在重复的 Windows 程序附件")
				}
				asset = &release.Assets[i]
			}
		}
	}
	if asset == nil {
		return updateRelease{}, fmt.Errorf("Release 缺少附件 %s", versionedName)
	}
	parsedURL, err := url.Parse(asset.BrowserDownloadURL)
	if err != nil || parsedURL.Scheme != "https" || !strings.EqualFold(parsedURL.Hostname(), "github.com") {
		return updateRelease{}, errors.New("Release 附件下载地址无效")
	}
	if asset.Size <= 0 || asset.Size > maxUpdateBytes {
		return updateRelease{}, errors.New("Release 附件大小无效或超过 64 MiB")
	}
	digest, err := parseSHA256Digest(asset.Digest)
	if err != nil {
		return updateRelease{}, err
	}
	return updateRelease{Tag: release.TagName, URL: parsedURL.String(), Digest: digest, Size: asset.Size}, nil
}

func versionedUpdateAssetName(tag string) string {
	return "quota-monitor-" + tag + ".exe"
}

func parseSHA256Digest(value string) (string, error) {
	if !strings.HasPrefix(value, "sha256:") {
		return "", errors.New("Release 附件没有有效的 SHA-256 摘要")
	}
	digest := strings.TrimPrefix(value, "sha256:")
	decoded, err := hex.DecodeString(digest)
	if err != nil || len(decoded) != sha256.Size {
		return "", errors.New("Release 附件的 SHA-256 摘要格式无效")
	}
	return strings.ToLower(digest), nil
}

func downloadAndVerify(client *http.Client, release updateRelease) (string, error) {
	if release.Size <= 0 || release.Size > maxUpdateBytes {
		return "", errors.New("更新附件大小无效")
	}
	wantDigest, err := parseSHA256Digest("sha256:" + release.Digest)
	if err != nil {
		return "", err
	}
	request, err := http.NewRequest(http.MethodGet, release.URL, nil)
	if err != nil {
		return "", err
	}
	request.Header.Set("User-Agent", "CPA-Quota-Query/"+appVersion)
	response, err := client.Do(request)
	if err != nil {
		return "", fmt.Errorf("下载更新失败：%w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return "", fmt.Errorf("下载更新返回 HTTP %d", response.StatusCode)
	}
	if response.ContentLength > maxUpdateBytes {
		return "", errors.New("更新附件超过 64 MiB")
	}

	temporary, err := os.CreateTemp("", "cpa-quota-update-*.exe")
	if err != nil {
		return "", fmt.Errorf("无法创建更新临时文件：%w", err)
	}
	path := temporary.Name()
	keep := false
	defer func() {
		_ = temporary.Close()
		if !keep {
			_ = os.Remove(path)
		}
	}()

	hasher := sha256.New()
	written, err := io.Copy(io.MultiWriter(temporary, hasher), io.LimitReader(response.Body, maxUpdateBytes+1))
	if err != nil {
		return "", fmt.Errorf("保存更新文件失败：%w", err)
	}
	if written > maxUpdateBytes || written != release.Size {
		return "", errors.New("下载附件大小与 Release 元数据不符")
	}
	if gotDigest := hex.EncodeToString(hasher.Sum(nil)); gotDigest != wantDigest {
		return "", errors.New("更新文件 SHA-256 校验失败")
	}
	if err := temporary.Sync(); err != nil {
		return "", fmt.Errorf("写入更新文件失败：%w", err)
	}
	if err := temporary.Close(); err != nil {
		return "", fmt.Errorf("关闭更新文件失败：%w", err)
	}
	keep = true
	return path, nil
}

func parseVersion(value string) ([3]uint64, error) {
	var result [3]uint64
	if !strings.HasPrefix(value, "v") {
		return result, fmt.Errorf("版本号必须以 v 开头：%q", value)
	}
	parts := strings.Split(strings.TrimPrefix(value, "v"), ".")
	if len(parts) != 2 && len(parts) != 3 {
		return result, fmt.Errorf("版本号格式无效：%q", value)
	}
	for i, part := range parts {
		if part == "" || (len(part) > 1 && part[0] == '0') {
			return result, fmt.Errorf("版本号格式无效：%q", value)
		}
		for _, digit := range part {
			if digit < '0' || digit > '9' {
				return result, fmt.Errorf("版本号格式无效：%q", value)
			}
		}
		number, err := strconv.ParseUint(part, 10, 64)
		if err != nil {
			return result, fmt.Errorf("版本号数值无效：%q", value)
		}
		result[i] = number
	}
	return result, nil
}

func compareVersions(left, right string) (int, error) {
	a, err := parseVersion(left)
	if err != nil {
		return 0, err
	}
	b, err := parseVersion(right)
	if err != nil {
		return 0, err
	}
	for i := range a {
		if a[i] < b[i] {
			return -1, nil
		}
		if a[i] > b[i] {
			return 1, nil
		}
	}
	return 0, nil
}

func (a *monitorApp) installUpdate() {
	if !a.installingUpdate.CompareAndSwap(false, true) {
		return
	}
	snapshot := a.snapshot()
	if !snapshot.updateAvailable {
		a.installingUpdate.Store(false)
		return
	}
	a.state.mu.Lock()
	a.state.updateStatus = "正在下载更新到 " + snapshot.updateRelease.Tag + "…"
	a.state.mu.Unlock()
	procPostMessage.Call(uintptr(a.mainWindow), wmStateChanged, 0, 0)

	go func(release updateRelease) {
		client := &http.Client{
			Timeout: 2 * time.Minute,
			CheckRedirect: func(request *http.Request, via []*http.Request) error {
				if len(via) >= 5 {
					return errors.New("更新下载重定向次数过多")
				}
				if len(via) > 0 && request.URL.Scheme != via[len(via)-1].URL.Scheme {
					return errors.New("拒绝不安全的更新下载重定向")
				}
				return nil
			},
		}
		temporary, err := downloadAndVerify(client, release)
		if err != nil {
			a.finishUpdateError(err)
			return
		}
		executable, err := os.Executable()
		if err == nil {
			err = launchUpdateHelper(executable, temporary, os.Getpid())
		}
		if err != nil {
			_ = os.Remove(temporary)
			a.finishUpdateError(err)
			return
		}
		a.state.mu.Lock()
		a.state.updateStatus = "正在安装更新…"
		a.state.mu.Unlock()
		procPostMessage.Call(uintptr(a.mainWindow), wmStateChanged, 0, 0)
		procPostMessage.Call(uintptr(a.mainWindow), wmClose, 0, 0)
	}(snapshot.updateRelease)
}

func (a *monitorApp) finishUpdateError(err error) {
	a.installingUpdate.Store(false)
	a.state.mu.Lock()
	a.state.updateStatus = "更新失败：" + shortError(err.Error())
	a.state.mu.Unlock()
	procPostMessage.Call(uintptr(a.mainWindow), wmStateChanged, 0, 0)
}

func launchUpdateHelper(target, download string, processID int) error {
	target, err := filepath.Abs(target)
	if err != nil {
		return err
	}
	download, err = filepath.Abs(download)
	if err != nil {
		return err
	}
	permissionErr := checkDirectoryWritable(filepath.Dir(target))
	if permissionErr != nil && !os.IsPermission(permissionErr) {
		return fmt.Errorf("无法准备程序安装目录：%w", permissionErr)
	}
	powershell, err := exec.LookPath("powershell.exe")
	if err != nil {
		return errors.New("找不到 Windows PowerShell，无法完成程序替换")
	}
	encoded := encodePowerShellCommand(updateHelperScript(target, download, processID))
	arguments := []string{"-NoProfile", "-NonInteractive", "-WindowStyle", "Hidden", "-ExecutionPolicy", "Bypass", "-EncodedCommand", encoded}

	if permissionErr == nil {
		command := exec.Command(powershell, arguments...)
		command.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
		if err := command.Start(); err == nil {
			return nil
		} else if !os.IsPermission(err) {
			return fmt.Errorf("启动更新辅助进程失败：%w", err)
		}
	}
	return startElevatedPowerShell(powershell, arguments, filepath.Dir(target))
}

func checkDirectoryWritable(directory string) error {
	temporary, err := os.CreateTemp(directory, ".cpa-quota-write-test-*")
	if err != nil {
		return err
	}
	name := temporary.Name()
	if err := temporary.Close(); err != nil {
		_ = os.Remove(name)
		return err
	}
	return os.Remove(name)
}

func startElevatedPowerShell(executable string, arguments []string, workingDirectory string) error {
	verb, _ := syscall.UTF16PtrFromString("runas")
	file, err := syscall.UTF16PtrFromString(executable)
	if err != nil {
		return err
	}
	params, err := syscall.UTF16PtrFromString(strings.Join(arguments, " "))
	if err != nil {
		return err
	}
	directory, err := syscall.UTF16PtrFromString(workingDirectory)
	if err != nil {
		return err
	}
	result, _, callErr := procShellExecute.Call(0,
		uintptr(unsafe.Pointer(verb)), uintptr(unsafe.Pointer(file)), uintptr(unsafe.Pointer(params)),
		uintptr(unsafe.Pointer(directory)), swHide)
	if result <= 32 {
		if result == 1223 {
			return errors.New("用户取消了管理员权限请求，程序未更新")
		}
		if callErr != syscall.Errno(0) {
			return fmt.Errorf("请求管理员权限失败：%w", callErr)
		}
		return fmt.Errorf("请求管理员权限失败（代码 %d）", result)
	}
	return nil
}

func updateHelperScript(target, download string, processID int) string {
	targetLiteral := powershellLiteral(target)
	downloadLiteral := powershellLiteral(download)
	markerLiteral := powershellLiteral(updateFailureMarker(target))
	return fmt.Sprintf(`$ErrorActionPreference = 'Stop'
$target = %s
$download = %s
$marker = %s
$elevated = ([Security.Principal.WindowsPrincipal][Security.Principal.WindowsIdentity]::GetCurrent()).IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)
try {
  Wait-Process -Id %d -ErrorAction SilentlyContinue
  Start-Sleep -Milliseconds 400
  $staging = $target + '.update-new'
  $backup = $target + '.update-backup'
  if (Test-Path -LiteralPath $staging) { Remove-Item -LiteralPath $staging -Force }
  if (Test-Path -LiteralPath $backup) { Remove-Item -LiteralPath $backup -Force }
  Copy-Item -LiteralPath $download -Destination $staging -Force
  [System.IO.File]::Replace($staging, $target, $backup, $true)
  if ($elevated) {
    Start-Process -FilePath (Join-Path $env:WINDIR 'explorer.exe') -ArgumentList ('"' + $target + '"')
  } else {
    Start-Process -FilePath $target
  }
  Remove-Item -LiteralPath $download -Force -ErrorAction SilentlyContinue
  Remove-Item -LiteralPath $backup -Force -ErrorAction SilentlyContinue
  Remove-Item -LiteralPath $marker -Force -ErrorAction SilentlyContinue
} catch {
  if (Test-Path -LiteralPath $backup) {
    try {
      if (Test-Path -LiteralPath $target) { Remove-Item -LiteralPath $target -Force }
      Move-Item -LiteralPath $backup -Destination $target -Force
    } catch {}
  }
  try { Set-Content -LiteralPath $marker -Value '更新安装失败，请检查目录权限后重试。' -Encoding UTF8 }
  catch {}
  if (Test-Path -LiteralPath $target) {
    if ($elevated) {
      Start-Process -FilePath (Join-Path $env:WINDIR 'explorer.exe') -ArgumentList ('"' + $target + '"')
    } else {
      Start-Process -FilePath $target
    }
  }
}
`, targetLiteral, downloadLiteral, markerLiteral, processID)
}

func powershellLiteral(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}

func encodePowerShellCommand(script string) string {
	units := utf16.Encode([]rune(script))
	data := make([]byte, len(units)*2)
	for i, unit := range units {
		data[i*2] = byte(unit)
		data[i*2+1] = byte(unit >> 8)
	}
	return base64.StdEncoding.EncodeToString(data)
}

func updateFailureMarker(executable string) string {
	return executable + ".update-error"
}

func consumeUpdateFailure(executable string) string {
	path := updateFailureMarker(executable)
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	_ = os.Remove(path)
	return strings.TrimSpace(strings.TrimPrefix(string(data), "\uFEFF"))
}
