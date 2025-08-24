package main

import (
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	ColorReset  = "\033[0m"
	ColorRed    = "\033[31m"
	ColorGreen  = "\033[32m"
	ColorYellow = "\033[33m"
	ColorBlue   = "\033[34m"
	ColorPurple = "\033[35m"
	ColorCyan   = "\033[36m"
	ColorWhite  = "\033[37m"
	ColorBold   = "\033[1m"
)

type FileInfo struct {
	Path    string
	Size    int64
	ModTime int64
	Mode    os.FileMode
	Uid     uint32
	Gid     uint32
}

type DirectoryMonitor struct {
	watchDir      string
	baseDir       string
	backupDir     string
	isolateDir    string
	extensions    []string
	baseline      map[string]FileInfo
	directories   []string
	checkInterval time.Duration
	apiEndpoint   string
	mu            sync.RWMutex
}

type MonitorConfig struct {
	WatchDir    string
	BaseDir     string
	Extensions  []string
	APIEndpoint string
}

func NewDirectoryMonitor(config MonitorConfig) *DirectoryMonitor {
	timestamp := time.Now().Format("20060102_150405")

	return &DirectoryMonitor{
		watchDir:      config.WatchDir,
		baseDir:       config.BaseDir,
		backupDir:     filepath.Join(config.BaseDir, fmt.Sprintf("backup_%s", timestamp)),
		isolateDir:    filepath.Join(config.BaseDir, fmt.Sprintf("isolate_%s", timestamp)),
		extensions:    config.Extensions,
		baseline:      make(map[string]FileInfo),
		checkInterval: 200 * time.Millisecond, // 硬编码为200ms，快速响应
		apiEndpoint:   config.APIEndpoint,
	}
}

func logInfo(msg string) {
	log.Printf("%s[INFO]%s %s", ColorGreen, ColorReset, msg)
}

func logWarn(msg string) {
	log.Printf("%s[WARN]%s %s", ColorYellow, ColorReset, msg)
}

func logError(msg string) {
	log.Printf("%s[ERROR]%s %s", ColorRed, ColorReset, msg)
}

func logSuccess(msg string) {
	log.Printf("%s[SUCCESS]%s %s", ColorGreen+ColorBold, ColorReset, msg)
}

func logAlert(msg string) {
	log.Printf("%s[ALERT]%s %s", ColorRed+ColorBold, ColorReset, msg)
}

func logDebug(msg string) {
	log.Printf("%s[DEBUG]%s %s", ColorCyan, ColorReset, msg)
}

func (dm *DirectoryMonitor) sendAPIAlert(alertType, message string) {
	if dm.apiEndpoint == "" {
		return
	}

	apiURL := fmt.Sprintf("http://%s/api/agent/edr-alert?type=%s&message=%s",
		dm.apiEndpoint, alertType, url.QueryEscape(message))

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(apiURL)
	if err != nil {
		logError(fmt.Sprintf("API告警发送失败: %v", err))
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode == 200 {
		logSuccess(fmt.Sprintf("告警发送成功: %s", message))
	} else {
		logError(fmt.Sprintf("告警响应异常: HTTP %d", resp.StatusCode))
	}
}

func (dm *DirectoryMonitor) shouldMonitorFile(filename string) bool {
	if len(dm.extensions) == 0 {
		return true
	}

	ext := strings.ToLower(filepath.Ext(filename))
	for _, allowedExt := range dm.extensions {
		if ext == strings.ToLower(allowedExt) {
			return true
		}
	}
	return false
}

func (dm *DirectoryMonitor) isRegularFile(filePath string) bool {
	info, err := os.Lstat(filePath) // 使用Lstat不跟随符号链接
	if err != nil {
		return false
	}

	return info.Mode().IsRegular()
}

func (dm *DirectoryMonitor) getFileInfo(filePath string) (FileInfo, error) {
	info, err := os.Stat(filePath)
	if err != nil {
		return FileInfo{}, err
	}

	sys := info.Sys().(*syscall.Stat_t)

	return FileInfo{
		Path:    filePath,
		Size:    info.Size(),
		ModTime: info.ModTime().Unix(),
		Mode:    info.Mode(),
		Uid:     sys.Uid,
		Gid:     sys.Gid,
	}, nil
}

func (dm *DirectoryMonitor) validatePaths() error {
	watchAbs, err := filepath.Abs(dm.watchDir)
	if err != nil {
		return fmt.Errorf("获取监控目录绝对路径失败: %v", err)
	}

	baseAbs, err := filepath.Abs(dm.baseDir)
	if err != nil {
		return fmt.Errorf("获取基础目录绝对路径失败: %v", err)
	}

	relPath, err := filepath.Rel(watchAbs, baseAbs)
	if err == nil && !strings.HasPrefix(relPath, "..") {
		return fmt.Errorf("错误: 备份目录不能在监控目录内\n监控目录: %s\n备份目录: %s",
			watchAbs, baseAbs)
	}

	logSuccess("路径验证通过")
	logInfo(fmt.Sprintf("监控目录: %s", watchAbs))
	logInfo(fmt.Sprintf("备份目录: %s", dm.backupDir))
	logInfo(fmt.Sprintf("隔离目录: %s", dm.isolateDir))

	return nil
}

func (dm *DirectoryMonitor) discoverDirectories() error {
	directories := make(map[string]bool)

	err := filepath.Walk(dm.watchDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		if info.IsDir() {
			directories[path] = true
		}
		return nil
	})

	if err != nil {
		return err
	}

	dm.directories = make([]string, 0, len(directories))
	for dir := range directories {
		dm.directories = append(dm.directories, dir)
	}

	logInfo(fmt.Sprintf("发现 %d 个目录需要监控", len(dm.directories)))
	return nil
}

func (dm *DirectoryMonitor) backupFile(srcPath string) error {
	if !dm.isRegularFile(srcPath) {
		logDebug(fmt.Sprintf("跳过非常规文件: %s", srcPath))
		return nil
	}

	relPath, err := filepath.Rel(dm.watchDir, srcPath)
	if err != nil {
		return err
	}

	dstPath := filepath.Join(dm.backupDir, relPath)

	dstDir := filepath.Dir(dstPath)
	if err := os.MkdirAll(dstDir, 0755); err != nil {
		return err
	}

	srcInfo, err := dm.getFileInfo(srcPath)
	if err != nil {
		return err
	}

	src, err := os.Open(srcPath)
	if err != nil {
		return err
	}
	defer src.Close()

	dst, err := os.Create(dstPath)
	if err != nil {
		return err
	}
	defer dst.Close()

	if _, err = io.Copy(dst, src); err != nil {
		return err
	}

	if err := dm.restoreFileAttributes(dstPath, srcInfo); err != nil {
		logWarn(fmt.Sprintf("恢复备份文件属性失败 %s: %v", dstPath, err))
	}

	return nil
}

func (dm *DirectoryMonitor) restoreFileAttributes(filePath string, fileInfo FileInfo) error {
	if err := os.Chmod(filePath, fileInfo.Mode); err != nil {
		return fmt.Errorf("设置权限失败: %v", err)
	}

	if err := os.Chown(filePath, int(fileInfo.Uid), int(fileInfo.Gid)); err != nil {
		logDebug(fmt.Sprintf("设置文件所有者失败 %s: %v", filePath, err))
		// 不返回错误，因为非root用户通常无法修改所有者
	}

	modTime := time.Unix(fileInfo.ModTime, 0)
	if err := os.Chtimes(filePath, modTime, modTime); err != nil {
		return fmt.Errorf("设置修改时间失败: %v", err)
	}

	return nil
}

func (dm *DirectoryMonitor) backupAllFiles() error {
	logInfo("开始备份所有文件...")

	// 创建备份目录
	if err := os.MkdirAll(dm.backupDir, 0755); err != nil {
		return fmt.Errorf("创建备份目录失败: %v", err)
	}

	fileCount := 0
	err := filepath.Walk(dm.watchDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		if !info.IsDir() && dm.shouldMonitorFile(path) && dm.isRegularFile(path) {
			if err := dm.backupFile(path); err != nil {
				logError(fmt.Sprintf("备份文件失败 %s: %v", path, err))
				return err
			}
			fileCount++
		}
		return nil
	})

	if err != nil {
		return err
	}

	logSuccess(fmt.Sprintf("备份完成，共备份 %d 个文件", fileCount))
	return nil
}

func (dm *DirectoryMonitor) buildBaseline() error {
	baseline := make(map[string]FileInfo)

	err := filepath.Walk(dm.watchDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		if !info.IsDir() && dm.shouldMonitorFile(path) && dm.isRegularFile(path) {
			fileInfo, err := dm.getFileInfo(path)
			if err != nil {
				logError(fmt.Sprintf("获取文件信息失败 %s: %v", path, err))
				return err
			}
			baseline[path] = fileInfo
		}
		return nil
	})

	if err != nil {
		return err
	}

	dm.mu.Lock()
	dm.baseline = baseline
	dm.mu.Unlock()

	logSuccess(fmt.Sprintf("基线建立完成，共 %d 个文件", len(baseline)))
	return nil
}

func (dm *DirectoryMonitor) restoreFile(filePath string) error {
	relPath, err := filepath.Rel(dm.watchDir, filePath)
	if err != nil {
		return err
	}

	backupPath := filepath.Join(dm.backupDir, relPath)

	if _, err := os.Stat(backupPath); os.IsNotExist(err) {
		return fmt.Errorf("备份文件不存在: %s", backupPath)
	}

	dm.mu.RLock()
	baselineInfo, exists := dm.baseline[filePath]
	dm.mu.RUnlock()

	if !exists {
		return fmt.Errorf("基线中未找到文件信息: %s", filePath)
	}

	src, err := os.Open(backupPath)
	if err != nil {
		return err
	}
	defer src.Close()

	dst, err := os.Create(filePath)
	if err != nil {
		return err
	}
	defer dst.Close()

	if _, err = io.Copy(dst, src); err != nil {
		return err
	}

	if err := dm.restoreFileAttributes(filePath, baselineInfo); err != nil {
		return fmt.Errorf("恢复文件属性失败: %v", err)
	}

	logSuccess(fmt.Sprintf("文件已完整还原: %s", filePath))
	return nil
}

func (dm *DirectoryMonitor) isolateFile(filePath string) error {
	// 创建隔离目录
	if err := os.MkdirAll(dm.isolateDir, 0755); err != nil {
		return fmt.Errorf("创建隔离目录失败: %v", err)
	}

	timestamp := time.Now().Format("20060102_150405_000")
	filename := fmt.Sprintf("%s_%s_%s",
		timestamp,
		filepath.Base(filePath),
		strings.ReplaceAll(filepath.Dir(filePath), "/", "_"))

	isolatedPath := filepath.Join(dm.isolateDir, filename)

	if err := os.Rename(filePath, isolatedPath); err != nil {
		return fmt.Errorf("移动文件到隔离目录失败: %v", err)
	}

	logSuccess(fmt.Sprintf("可疑文件已隔离: %s", filepath.Base(filePath)))
	return nil
}

func (dm *DirectoryMonitor) getDirectChildren(dirPath string) ([]string, error) {
	entries, err := os.ReadDir(dirPath)
	if err != nil {
		return nil, err
	}

	var files []string
	for _, entry := range entries {
		if !entry.IsDir() {
			fullPath := filepath.Join(dirPath, entry.Name())
			if dm.shouldMonitorFile(fullPath) && dm.isRegularFile(fullPath) {
				files = append(files, fullPath)
			}
		}
	}

	return files, nil
}

func (dm *DirectoryMonitor) monitorDirectory(dirPath string, wg *sync.WaitGroup) {
	defer wg.Done()

	ticker := time.NewTicker(dm.checkInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			dm.checkDirectoryChanges(dirPath)
		}
	}
}

func (dm *DirectoryMonitor) checkDirectoryChanges(dirPath string) {
	currentFiles, err := dm.getDirectChildren(dirPath)
	if err != nil {
		logError(fmt.Sprintf("读取目录失败 %s: %v", dirPath, err))
		return
	}

	dm.mu.RLock()
	baseline := dm.baseline
	dm.mu.RUnlock()

	currentFileMap := make(map[string]FileInfo)
	for _, filePath := range currentFiles {
		fileInfo, err := dm.getFileInfo(filePath)
		if err != nil {
			logError(fmt.Sprintf("获取文件信息失败 %s: %v", filePath, err))
			continue
		}
		currentFileMap[filePath] = fileInfo
	}

	for filePath, currentInfo := range currentFileMap {
		if baselineInfo, exists := baseline[filePath]; !exists {
			alertMsg := fmt.Sprintf("检测到新增可疑文件: %s (大小: %d bytes)",
				filepath.Base(filePath), currentInfo.Size)
			logAlert(alertMsg)

			dm.sendAPIAlert("warning", alertMsg)

			if err := dm.isolateFile(filePath); err != nil {
				logError(fmt.Sprintf("隔离新增文件失败: %v", err))
			}
		} else {
			if currentInfo.Size != baselineInfo.Size ||
				currentInfo.ModTime != baselineInfo.ModTime ||
				currentInfo.Mode != baselineInfo.Mode {

				alertMsg := fmt.Sprintf("检测到文件被修改: %s", filepath.Base(filePath))
				logAlert(alertMsg)

				dm.sendAPIAlert("warning", alertMsg)

				logInfo(fmt.Sprintf("修改详情 - 原始: 大小=%d, 时间=%d, 权限=%v",
					baselineInfo.Size, baselineInfo.ModTime, baselineInfo.Mode))
				logInfo(fmt.Sprintf("修改详情 - 当前: 大小=%d, 时间=%d, 权限=%v",
					currentInfo.Size, currentInfo.ModTime, currentInfo.Mode))

				if err := dm.isolateFile(filePath); err != nil {
					logError(fmt.Sprintf("隔离被修改文件失败: %v", err))
				}

				if err := dm.restoreFile(filePath); err != nil {
					logError(fmt.Sprintf("还原文件失败: %v", err))
				}
			}
		}
	}

	for filePath := range baseline {
		if filepath.Dir(filePath) == dirPath {
			if _, exists := currentFileMap[filePath]; !exists {
				alertMsg := fmt.Sprintf("检测到文件被删除: %s", filepath.Base(filePath))
				logAlert(alertMsg)

				dm.sendAPIAlert("warning", alertMsg)

				if err := dm.restoreFile(filePath); err != nil {
					logError(fmt.Sprintf("还原被删除的文件失败: %v", err))
				}
			}
		}
	}
}

func (dm *DirectoryMonitor) Start() error {
	if err := dm.validatePaths(); err != nil {
		return err
	}

	if err := dm.discoverDirectories(); err != nil {
		return fmt.Errorf("发现目录失败: %v", err)
	}

	if err := dm.backupAllFiles(); err != nil {
		return fmt.Errorf("备份文件失败: %v", err)
	}

	if err := dm.buildBaseline(); err != nil {
		return fmt.Errorf("建立基线失败: %v", err)
	}

	if err := os.MkdirAll(dm.isolateDir, 0755); err != nil {
		return fmt.Errorf("创建隔离目录失败: %v", err)
	}

	logInfo(fmt.Sprintf("启动 %d 个监控goroutine，检测间隔: %v",
		len(dm.directories), dm.checkInterval))

	if dm.apiEndpoint != "" {
		logInfo(fmt.Sprintf("API端点: http://%s", dm.apiEndpoint))
	} else {
		logInfo("API端点: 未配置（仅本地日志）")
	}

	var wg sync.WaitGroup
	for _, dir := range dm.directories {
		wg.Add(1)
		go dm.monitorDirectory(dir, &wg)
	}

	logSuccess("EDR监控已启动，正在监控文件变化...")
	wg.Wait()

	return nil
}

func parseExtensions(extStr string) []string {
	if extStr == "" {
		return nil
	}

	parts := strings.Split(extStr, ",")
	var extensions []string

	for _, part := range parts {
		ext := strings.TrimSpace(part)
		if ext != "" {
			if !strings.HasPrefix(ext, ".") {
				ext = "." + ext
			}
			extensions = append(extensions, ext)
		}
	}

	return extensions
}

func main() {
	var (
		monitorDir  = flag.String("m", "", "监控目录路径 (必需)")
		baseDir     = flag.String("b", "", "基础目录路径，将在此目录下创建backup_和isolate_子目录 (必需)")
		extensions  = flag.String("e", "", "监控的文件扩展名，用逗号分隔 (例如: .php,.js,.html)")
		apiEndpoint = flag.String("a", "", "API端点地址 (例如: 192.168.1.100:8080), 不指定则不发送")
		help        = flag.Bool("h", false, "显示帮助信息")
	)

	flag.Parse()

	if *help {
		fmt.Printf("%sEDR 文件完整性监控器 v2.1%s\n", ColorBold, ColorReset)
		fmt.Println("")
		fmt.Printf("%s用法:%s\n", ColorYellow, ColorReset)
		fmt.Println("  ./edr -m /var/www/html -b /tmp/edr_workspace -e .php,.jsp")
		fmt.Println("  ./edr -m /var/www/html -b /tmp/edr_workspace -e .php -a 192.168.1.100:8080")
		fmt.Println("")
		fmt.Printf("%s参数:%s\n", ColorYellow, ColorReset)
		flag.PrintDefaults()
		fmt.Println("")
		fmt.Printf("%s目录结构:%s\n", ColorYellow, ColorReset)
		fmt.Println("  基础目录/")
		fmt.Println("  ├── backup_20250821_143022/   # 备份目录")
		fmt.Println("  └── isolate_20250821_143022/  # 隔离目录")
		fmt.Println("")
		return
	}

	if *monitorDir == "" || *baseDir == "" {
		logError("必须指定监控目录(-m)和基础目录(-b)")
		os.Exit(1)
	}

	if _, err := os.Stat(*monitorDir); os.IsNotExist(err) {
		logError(fmt.Sprintf("监控目录不存在: %s", *monitorDir))
		os.Exit(1)
	}

	if err := os.MkdirAll(*baseDir, 0755); err != nil {
		logError(fmt.Sprintf("创建基础目录失败: %v", err))
		os.Exit(1)
	}

	extList := parseExtensions(*extensions)
	config := MonitorConfig{
		WatchDir:    *monitorDir,
		BaseDir:     *baseDir,
		Extensions:  extList,
		APIEndpoint: *apiEndpoint,
	}

	logo := `   ___  _____        __     _______         __          _______  
  / _ \|  __ \     /\\ \   / / ____|       /\ \        / /  __ \ 
 | | | | |__) |   /  \\ \_/ / (___ ______ /  \ \  /\  / /| |  | |
 | | | |  _  /   / /\ \\   / \___ \______/ /\ \ \/  \/ / | |  | |
 | |_| | | \ \  / ____ \| |  ____) |    / ____ \  /\  /  | |__| |
  \___/|_|  \_\/_/    \_\_| |_____/    /_/    \_\/  \/   |_____/ 
                                                                 
                                                                 `
	fmt.Println(logo)
	fmt.Printf("%s========================================%s\n", ColorBlue, ColorReset)
	fmt.Printf("%s0RAYS EDR 文件完整性监控器%s\n", ColorBold, ColorReset)
	fmt.Printf("%s========================================%s\n", ColorBlue, ColorReset)
	logInfo(fmt.Sprintf("监控目录: %s", config.WatchDir))
	logInfo(fmt.Sprintf("基础目录: %s", config.BaseDir))
	if len(extList) > 0 {
		logInfo(fmt.Sprintf("监控扩展名: %v", extList))
	} else {
		logInfo("监控扩展名: 所有文件")
	}
	if *apiEndpoint != "" {
		logInfo(fmt.Sprintf("API端点: http://%s", *apiEndpoint))
	} else {
		logInfo("API端点: 未配置")
	}
	fmt.Printf("%s========================================%s\n", ColorBlue, ColorReset)

	monitor := NewDirectoryMonitor(config)

	if err := monitor.Start(); err != nil {
		logError(fmt.Sprintf("启动监控失败: %v", err))
		os.Exit(1)
	}
}
