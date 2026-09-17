package main

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	singBoxNodesDirDefault               = "singbox_nodes"
	singBoxEngineConfigFile              = "singbox-engine.json"
	singBoxDefaultRepeat                 = 3
	singBoxDefaultTimeout                = 8 * time.Second
	singBoxDefaultUpdateInterval         = 6 * time.Hour
	singBoxDefaultProviderInterval       = "1h"
	singBoxDefaultDownloadBytes    int64 = 10 << 20
	singBoxMaxRepeat                     = 10
	singBoxMaxBatchNodes                 = 3000
	singBoxSelectorTag                   = "cfdata-select"
	singBoxTunTag                        = "cfdata-tun"
	singBoxMixedTag                      = "cfdata-mixed"
	singBoxControllerSecretBytes         = 18
)

type singBoxConfiguredSubscription struct {
	Name    string            `json:"name"`
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers,omitempty"`
}

type singBoxEngineConfig struct {
	Version                int                             `json:"version"`
	NodesDir               string                          `json:"nodes_dir"`
	UpdateIntervalHours    int                             `json:"update_interval_hours"`
	StartupSync            bool                            `json:"startup_sync"`
	ProviderUpdateInterval string                          `json:"provider_update_interval"`
	TestRepeat             int                             `json:"test_repeat"`
	TestTimeoutSeconds     int                             `json:"test_timeout_seconds"`
	DownloadTestBytes      int64                           `json:"download_test_bytes"`
	DownloadTestURL        string                          `json:"download_test_url"`
	PreferSingTun          bool                            `json:"prefer_sing_tun"`
	SingTunRequiresRoot    bool                            `json:"sing_tun_requires_root"`
	MixedFallback          bool                            `json:"mixed_fallback"`
	LocalProxyHost         string                          `json:"local_proxy_host"`
	ControllerHost         string                          `json:"controller_host"`
	Subscriptions          []singBoxConfiguredSubscription `json:"subscriptions,omitempty"`
}

type singBoxNodeSource struct {
	SubscriptionID   string `json:"subscriptionId"`
	SubscriptionName string `json:"subscriptionName"`
	SubscriptionURL  string `json:"subscriptionUrl,omitempty"`
	ProviderTag      string `json:"providerTag,omitempty"`
	NodeTag          string `json:"nodeTag,omitempty"`
	Raw              string `json:"raw,omitempty"`
}

type singBoxNodeVariant struct {
	Protocol    string                 `json:"protocol"`
	Name        string                 `json:"name"`
	Server      string                 `json:"server"`
	Port        int                    `json:"port"`
	Provider    string                 `json:"provider"`
	OutboundTag string                 `json:"outboundTag"`
	Outbound    map[string]interface{} `json:"outbound"`
	Sources     []singBoxNodeSource    `json:"sources"`
}

type singBoxCachedNode struct {
	ID          string                 `json:"id"`
	Server      string                 `json:"server"`
	Port        int                    `json:"port"`
	Protocol    string                 `json:"protocol"`
	Name        string                 `json:"name"`
	Provider    string                 `json:"provider"`
	OutboundTag string                 `json:"outboundTag"`
	Outbound    map[string]interface{} `json:"outbound"`
	Sources     []singBoxNodeSource    `json:"sources"`
	Variants    []singBoxNodeVariant   `json:"variants,omitempty"`
}

type singBoxNodeView struct {
	ID           string              `json:"id"`
	Server       string              `json:"server"`
	Port         int                 `json:"port"`
	Protocol     string              `json:"protocol"`
	Name         string              `json:"name"`
	Provider     string              `json:"provider"`
	OutboundTag  string              `json:"outboundTag"`
	Sources      []singBoxNodeSource `json:"sources"`
	SourceCount  int                 `json:"sourceCount"`
	VariantCount int                 `json:"variantCount"`
	LastTest     *singBoxNodeResult  `json:"lastTest,omitempty"`
}

type singBoxNodeResult struct {
	NodeID        string               `json:"nodeId"`
	Node          string               `json:"node"`
	Protocol      string               `json:"protocol"`
	Server        string               `json:"server"`
	Port          int                  `json:"port"`
	Success       bool                 `json:"success"`
	SuccessCount  int                  `json:"successCount"`
	TotalAttempts int                  `json:"totalAttempts"`
	LossRate      float64              `json:"lossRate"`
	MinLatencyMS  int64                `json:"minLatencyMs"`
	MaxLatencyMS  int64                `json:"maxLatencyMs"`
	AvgLatencyMS  float64              `json:"avgLatencyMs"`
	Speed         string               `json:"speed,omitempty"`
	OutboundIP    string               `json:"outboundIP,omitempty"`
	WorkingSource string               `json:"workingSource,omitempty"`
	Mode          string               `json:"mode"`
	Results       []singBoxProbeResult `json:"results"`
	Error         string               `json:"error,omitempty"`
}

type singBoxProbeResult struct {
	Attempt    int    `json:"attempt"`
	Success    bool   `json:"success"`
	LatencyMS  int64  `json:"latencyMs"`
	StatusCode int    `json:"statusCode,omitempty"`
	OutboundIP string `json:"outboundIP,omitempty"`
	Error      string `json:"error,omitempty"`
}

type singBoxSyncResponse struct {
	Success       bool     `json:"success"`
	Subscriptions int      `json:"subscriptions"`
	Updated       int      `json:"updated"`
	Cached        int      `json:"cached"`
	Nodes         int      `json:"nodes"`
	Errors        []string `json:"errors,omitempty"`
	Error         string   `json:"error,omitempty"`
}

type singBoxTestRequest struct {
	NodeID         string `json:"nodeId"`
	Repeat         int    `json:"repeat"`
	TimeoutSeconds int    `json:"timeout"`
	DownloadBytes  int64  `json:"downloadBytes"`
	DownloadURL    string `json:"downloadUrl"`
	VariantIndex   int    `json:"variantIndex"`
}

type singBoxBatchTestRequest struct {
	NodeIDs        []string `json:"nodeIds"`
	Repeat         int      `json:"repeat"`
	TimeoutSeconds int      `json:"timeout"`
	DownloadBytes  int64    `json:"downloadBytes"`
	DownloadURL    string   `json:"downloadUrl"`
}

type singBoxBatchTestResponse struct {
	Success bool                `json:"success"`
	Total   int                 `json:"total"`
	Passed  int                 `json:"passed"`
	Results []singBoxNodeResult `json:"results"`
	Error   string              `json:"error,omitempty"`
}

type singBoxRuntime struct {
	process       *exec.Cmd
	configPath    string
	logPath       string
	mode          string
	controllerURL string
	secret        string
	proxyURL      string
	closeOnce     sync.Once
}

var singBoxState = struct {
	sync.RWMutex
	Results map[string]singBoxNodeResult
}{Results: map[string]singBoxNodeResult{}}

var singBoxSyncMu sync.Mutex

func init() {
	http.HandleFunc("/api/subscription/singbox/nodes", requireAuth(handleSingBoxNodes))
	http.HandleFunc("/api/subscription/singbox/sync-all", requireAuth(handleSingBoxSyncAll))
	http.HandleFunc("/api/subscription/singbox/test", requireAuth(handleSingBoxTest))
	http.HandleFunc("/api/subscription/singbox/test-batch", requireAuth(handleSingBoxBatchTest))
}

func dataDirectory() string {
	if value := strings.TrimSpace(os.Getenv("CFDATA_DATA_DIR")); value != "" {
		return value
	}
	if cwd, err := os.Getwd(); err == nil && strings.TrimSpace(cwd) != "" {
		return cwd
	}
	return os.TempDir()
}

func singBoxDataDir() string { return dataDirectory() }

func singBoxEngineConfigPath() string {
	return filepath.Join(singBoxDataDir(), singBoxEngineConfigFile)
}

func defaultSingBoxEngineConfig() singBoxEngineConfig {
	return singBoxEngineConfig{
		Version:                2,
		NodesDir:               singBoxNodesDirDefault,
		UpdateIntervalHours:    6,
		StartupSync:            true,
		ProviderUpdateInterval: singBoxDefaultProviderInterval,
		TestRepeat:             singBoxDefaultRepeat,
		TestTimeoutSeconds:     int(singBoxDefaultTimeout / time.Second),
		DownloadTestBytes:      singBoxDefaultDownloadBytes,
		DownloadTestURL:        "https://speed.cloudflare.com/__down?bytes=99999999",
		PreferSingTun:          true,
		SingTunRequiresRoot:    true,
		MixedFallback:          true,
		LocalProxyHost:         "127.0.0.1",
		ControllerHost:         "127.0.0.1",
		Subscriptions:          []singBoxConfiguredSubscription{},
	}
}

func loadSingBoxEngineConfig() (singBoxEngineConfig, error) {
	cfg := defaultSingBoxEngineConfig()
	if err := os.MkdirAll(singBoxDataDir(), 0700); err != nil {
		return cfg, fmt.Errorf("创建 sing-box 数据目录失败: %w", err)
	}
	path := singBoxEngineConfigPath()
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		data, marshalErr := json.MarshalIndent(cfg, "", "  ")
		if marshalErr != nil {
			return cfg, marshalErr
		}
		if writeErr := atomicWriteFile(path, data, 0600); writeErr != nil {
			return cfg, fmt.Errorf("创建 %s 失败: %w", singBoxEngineConfigFile, writeErr)
		}
		return cfg, nil
	}
	if err != nil {
		return cfg, err
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return cfg, fmt.Errorf("解析 %s 失败: %w", singBoxEngineConfigFile, err)
	}
	if strings.TrimSpace(cfg.NodesDir) == "" {
		cfg.NodesDir = singBoxNodesDirDefault
	}
	if cfg.UpdateIntervalHours <= 0 {
		cfg.UpdateIntervalHours = 6
	}
	if strings.TrimSpace(cfg.ProviderUpdateInterval) == "" {
		cfg.ProviderUpdateInterval = singBoxDefaultProviderInterval
	}
	if cfg.TestRepeat <= 0 || cfg.TestRepeat > singBoxMaxRepeat {
		cfg.TestRepeat = singBoxDefaultRepeat
	}
	if cfg.TestTimeoutSeconds <= 0 || cfg.TestTimeoutSeconds > 60 {
		cfg.TestTimeoutSeconds = int(singBoxDefaultTimeout / time.Second)
	}
	if cfg.DownloadTestBytes <= 0 {
		cfg.DownloadTestBytes = singBoxDefaultDownloadBytes
	}
	if strings.TrimSpace(cfg.DownloadTestURL) == "" {
		cfg.DownloadTestURL = "https://speed.cloudflare.com/__down?bytes=99999999"
	}
	if strings.TrimSpace(cfg.LocalProxyHost) == "" {
		cfg.LocalProxyHost = "127.0.0.1"
	}
	if strings.TrimSpace(cfg.ControllerHost) == "" {
		cfg.ControllerHost = "127.0.0.1"
	}
	return cfg, nil
}

func singBoxNodesDir(cfg singBoxEngineConfig) string {
	if filepath.IsAbs(cfg.NodesDir) {
		return cfg.NodesDir
	}
	return filepath.Join(singBoxDataDir(), cfg.NodesDir)
}

func singBoxNodeFileName(subscriptionName string) (string, error) {
	name := strings.TrimSpace(subscriptionName)
	if name == "" {
		return "", errors.New("订阅名称不能为空")
	}
	if name == "." || name == ".." || strings.ContainsAny(name, `/\\:*?"<>|`) {
		return "", errors.New("订阅名称包含文件名不允许的字符")
	}
	return name + ".json", nil
}

func singBoxNodeFilePath(cfg singBoxEngineConfig, subscriptionName string) (string, error) {
	fileName, err := singBoxNodeFileName(subscriptionName)
	if err != nil {
		return "", err
	}
	return filepath.Join(singBoxNodesDir(cfg), fileName), nil
}

func handleSingBoxNodes(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeSingBoxJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "Method Not Allowed"})
		return
	}
	cfg, err := loadSingBoxEngineConfig()
	if err != nil {
		writeSingBoxJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if cfg.StartupSync {
		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Minute)
		defer cancel()
		if _, err := syncAllSingBoxSubscriptions(ctx, false); err != nil {
			writeSingBoxJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
	}
	nodes, err := loadAggregatedSingBoxNodes()
	if err != nil {
		writeSingBoxJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	views := make([]singBoxNodeView, 0, len(nodes))
	for _, node := range nodes {
		view := singBoxNodeView{
			ID: node.ID, Server: node.Server, Port: node.Port, Protocol: node.Protocol,
			Name: node.Name, Provider: node.Provider, OutboundTag: node.OutboundTag,
			Sources: node.Sources, SourceCount: len(node.Sources), VariantCount: maxInt(1, len(node.Variants)),
		}
		singBoxState.RLock()
		if result, ok := singBoxState.Results[node.ID]; ok {
			copy := result
			view.LastTest = &copy
		}
		singBoxState.RUnlock()
		views = append(views, view)
	}
	writeSingBoxJSON(w, http.StatusOK, map[string]interface{}{"success": true, "nodes": views})
}

func handleSingBoxSyncAll(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeSingBoxJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "Method Not Allowed"})
		return
	}
	force := strings.EqualFold(r.URL.Query().Get("force"), "1") || strings.EqualFold(r.URL.Query().Get("force"), "true")
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
	defer cancel()
	result, err := syncAllSingBoxSubscriptions(ctx, force)
	if err != nil {
		result.Success = false
		result.Error = err.Error()
		writeSingBoxJSON(w, http.StatusInternalServerError, result)
		return
	}
	writeSingBoxJSON(w, http.StatusOK, result)
}

func handleSingBoxTest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeSingBoxJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "Method Not Allowed"})
		return
	}
	var req singBoxTestRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		writeSingBoxJSON(w, http.StatusBadRequest, map[string]string{"error": "请求格式错误: " + err.Error()})
		return
	}
	cfg, err := loadSingBoxEngineConfig()
	if err != nil {
		writeSingBoxJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if req.Repeat <= 0 || req.Repeat > singBoxMaxRepeat {
		req.Repeat = cfg.TestRepeat
	}
	if req.TimeoutSeconds <= 0 || req.TimeoutSeconds > 60 {
		req.TimeoutSeconds = cfg.TestTimeoutSeconds
	}
	if req.DownloadBytes < 0 {
		req.DownloadBytes = 0
	}
	if req.DownloadBytes == 0 {
		req.DownloadBytes = cfg.DownloadTestBytes
	}
	if strings.TrimSpace(req.DownloadURL) == "" {
		req.DownloadURL = cfg.DownloadTestURL
	}
	nodes, err := loadAggregatedSingBoxNodes()
	if err != nil {
		writeSingBoxJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	var target *singBoxCachedNode
	for i := range nodes {
		if nodes[i].ID == strings.TrimSpace(req.NodeID) {
			copy := nodes[i]
			target = &copy
			break
		}
	}
	if target == nil {
		writeSingBoxJSON(w, http.StatusNotFound, map[string]string{"error": "节点不存在，请先同步订阅"})
		return
	}
	result := testCachedSingBoxNode(r.Context(), cfg, *target, req.VariantIndex, req.Repeat, time.Duration(req.TimeoutSeconds)*time.Second, req.DownloadBytes, req.DownloadURL)
	singBoxState.Lock()
	singBoxState.Results[target.ID] = result
	singBoxState.Unlock()
	writeSingBoxJSON(w, http.StatusOK, result)
}

func handleSingBoxBatchTest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeSingBoxJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "Method Not Allowed"})
		return
	}
	var req singBoxBatchTestRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 4<<20)).Decode(&req); err != nil {
		writeSingBoxJSON(w, http.StatusBadRequest, map[string]string{"error": "请求格式错误: " + err.Error()})
		return
	}
	if len(req.NodeIDs) == 0 {
		writeSingBoxJSON(w, http.StatusBadRequest, map[string]string{"error": "没有选择节点"})
		return
	}
	if len(req.NodeIDs) > singBoxMaxBatchNodes {
		req.NodeIDs = req.NodeIDs[:singBoxMaxBatchNodes]
	}
	cfg, err := loadSingBoxEngineConfig()
	if err != nil {
		writeSingBoxJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if req.Repeat <= 0 || req.Repeat > singBoxMaxRepeat {
		req.Repeat = cfg.TestRepeat
	}
	if req.TimeoutSeconds <= 0 || req.TimeoutSeconds > 60 {
		req.TimeoutSeconds = cfg.TestTimeoutSeconds
	}
	if req.DownloadBytes <= 0 {
		req.DownloadBytes = cfg.DownloadTestBytes
	}
	if strings.TrimSpace(req.DownloadURL) == "" {
		req.DownloadURL = cfg.DownloadTestURL
	}
	nodes, err := loadAggregatedSingBoxNodes()
	if err != nil {
		writeSingBoxJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	wanted := make(map[string]struct{}, len(req.NodeIDs))
	for _, id := range req.NodeIDs {
		wanted[strings.TrimSpace(id)] = struct{}{}
	}
	selected := make([]singBoxCachedNode, 0, len(wanted))
	for _, node := range nodes {
		if _, ok := wanted[node.ID]; ok {
			selected = append(selected, node)
		}
	}
	if len(selected) == 0 {
		writeSingBoxJSON(w, http.StatusNotFound, map[string]string{"error": "所选节点不存在"})
		return
	}

	runtime, err := startSingBoxRuntime(r.Context(), cfg, selected)
	if err != nil {
		writeSingBoxJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	defer runtime.Close()

	results := make([]singBoxNodeResult, 0, len(selected))
	for _, node := range selected {
		result := testNodeInRuntime(r.Context(), runtime, cfg, node, req.Repeat, time.Duration(req.TimeoutSeconds)*time.Second, req.DownloadBytes, req.DownloadURL)
		singBoxState.Lock()
		singBoxState.Results[node.ID] = result
		singBoxState.Unlock()
		results = append(results, result)
		if r.Context().Err() != nil {
			break
		}
	}
	passed := 0
	for _, result := range results {
		if result.Success {
			passed++
		}
	}
	writeSingBoxJSON(w, http.StatusOK, singBoxBatchTestResponse{Success: passed == len(results), Total: len(results), Passed: passed, Results: results})
}

func mergeConfiguredSingBoxSubscriptions(configured []singBoxConfiguredSubscription) error {
	if len(configured) == 0 {
		return nil
	}
	if err := ensureSubscriptionStoreLoaded(); err != nil {
		return err
	}
	subscriptionStore.Lock()
	defer subscriptionStore.Unlock()
	changed := false
	seenNames := make(map[string]struct{}, len(configured))
	for _, configuredItem := range configured {
		name := strings.TrimSpace(configuredItem.Name)
		urlText := strings.TrimSpace(configuredItem.URL)
		if err := validateSubscriptionName(name); err != nil {
			return fmt.Errorf("配置订阅 %q: %w", name, err)
		}
		if err := validateSubscriptionURL(urlText); err != nil {
			return fmt.Errorf("配置订阅 %q: %w", name, err)
		}
		key := strings.ToLower(name)
		if _, ok := seenNames[key]; ok {
			return fmt.Errorf("singbox-engine.json 中存在重复订阅名称: %s", name)
		}
		seenNames[key] = struct{}{}
		found := -1
		for index := range subscriptionStore.Items {
			if strings.EqualFold(strings.TrimSpace(subscriptionStore.Items[index].Name), name) {
				found = index
				break
			}
		}
		headers := normalizeSubscriptionHeaders(configuredItem.Headers)
		if found < 0 {
			subscriptionStore.Items = append(subscriptionStore.Items, subscription{
				ID: fmt.Sprintf("sub-%d", time.Now().UnixNano()), Name: name, URL: urlText,
				Headers: headers, Status: "未更新", CreatedAt: time.Now().Format(time.RFC3339),
			})
			changed = true
		} else {
			item := &subscriptionStore.Items[found]
			if item.URL != urlText || !sameStringMap(item.Headers, headers) {
				item.URL = urlText
				item.Headers = headers
				changed = true
			}
		}
	}
	if !changed {
		return nil
	}
	return saveSubscriptionStoreLocked()
}

func sameStringMap(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for key, value := range a {
		if b[key] != value {
			return false
		}
	}
	return true
}

func syncAllSingBoxSubscriptions(ctx context.Context, force bool) (singBoxSyncResponse, error) {
	singBoxSyncMu.Lock()
	defer singBoxSyncMu.Unlock()

	cfg, err := loadSingBoxEngineConfig()
	if err != nil {
		return singBoxSyncResponse{}, err
	}
	if err := mergeConfiguredSingBoxSubscriptions(cfg.Subscriptions); err != nil {
		return singBoxSyncResponse{}, err
	}
	if err := os.MkdirAll(singBoxNodesDir(cfg), 0700); err != nil {
		return singBoxSyncResponse{}, fmt.Errorf("创建 sing-box 节点目录失败: %w", err)
	}
	if err := ensureSubscriptionStoreLoaded(); err != nil {
		return singBoxSyncResponse{}, err
	}
	subscriptionStore.Lock()
	items := append([]subscription(nil), subscriptionStore.Items...)
	subscriptionStore.Unlock()

	response := singBoxSyncResponse{Success: true, Subscriptions: len(items), Errors: []string{}}
	activeFiles := make(map[string]struct{}, len(items))
	interval := time.Duration(cfg.UpdateIntervalHours) * time.Hour

	for _, item := range items {
		filePath, pathErr := singBoxNodeFilePath(cfg, item.Name)
		if pathErr != nil {
			response.Errors = append(response.Errors, fmt.Sprintf("%s: %v", item.Name, pathErr))
			continue
		}
		activeFiles[filepath.Clean(filePath)] = struct{}{}

		needFetch := force
		if info, statErr := os.Stat(filePath); statErr != nil {
			needFetch = true
		} else if interval <= 0 || time.Since(info.ModTime()) >= interval {
			needFetch = true
		} else if !providerCacheLooksValid(filePath) {
			needFetch = true
		}

		if needFetch {
			if err := refreshSubscriptionProvider(ctx, cfg, item, filePath); err != nil {
				response.Errors = append(response.Errors, fmt.Sprintf("%s: Provider 更新失败: %v", item.Name, err))
				continue
			}
			response.Updated++
		} else {
			response.Cached++
		}
	}

	entries, _ := os.ReadDir(singBoxNodesDir(cfg))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") || entry.Name() == singBoxEngineConfigFile {
			continue
		}
		path := filepath.Clean(filepath.Join(singBoxNodesDir(cfg), entry.Name()))
		if _, ok := activeFiles[path]; !ok {
			_ = os.Remove(path)
		}
	}

	nodes, loadErr := loadAggregatedSingBoxNodes()
	if loadErr == nil {
		response.Nodes = len(nodes)
	}
	if len(response.Errors) > 0 {
		response.Success = response.Nodes > 0 || len(items) == 0
	}
	return response, nil
}

func refreshSubscriptionProvider(ctx context.Context, cfg singBoxEngineConfig, item subscription, path string) error {
	binary, err := findSingBoxBinary()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}

	backupPath := path + ".bak"
	_ = os.Remove(backupPath)
	if _, statErr := os.Stat(path); statErr == nil {
		if err := os.Rename(path, backupPath); err != nil {
			return fmt.Errorf("备份 Provider 缓存失败: %w", err)
		}
	}
	defer func() {
		_ = os.Remove(backupPath)
	}()

	configPath, logPath, err := createProviderSyncConfig(cfg, item, path)
	if err != nil {
		_ = restoreProviderBackup(path, backupPath)
		return err
	}
	defer os.Remove(configPath)
	defer os.Remove(logPath)

	cmd := exec.CommandContext(ctx, binary, "run", "-c", configPath)
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		_ = restoreProviderBackup(path, backupPath)
		return err
	}
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	if err := cmd.Start(); err != nil {
		logFile.Close()
		_ = restoreProviderBackup(path, backupPath)
		return err
	}

	deadline := time.Now().Add(45 * time.Second)
	var startErr error
	for time.Now().Before(deadline) {
		if ctx.Err() != nil {
			startErr = ctx.Err()
			break
		}
		if providerCacheLooksValid(path) {
			_ = terminateProcess(cmd)
			_ = logFile.Close()
			return nil
		}
		if cmd.ProcessState != nil && cmd.ProcessState.Exited() {
			startErr = fmt.Errorf("sing-box Provider 进程提前退出")
			break
		}
		time.Sleep(250 * time.Millisecond)
	}
	_ = terminateProcess(cmd)
	_ = logFile.Close()

	if startErr == nil {
		startErr = fmt.Errorf("等待 Provider 缓存生成超时")
	}
	logText, _ := os.ReadFile(logPath)
	if trimmed := strings.TrimSpace(string(logText)); trimmed != "" {
		startErr = fmt.Errorf("%w；核心日志: %s", startErr, lastLogLines(trimmed, 10))
	}
	_ = os.Remove(path)
	if restoreErr := restoreProviderBackup(path, backupPath); restoreErr != nil {
		return fmt.Errorf("%w；恢复旧缓存失败: %v", startErr, restoreErr)
	}
	return startErr
}

func createProviderSyncConfig(cfg singBoxEngineConfig, item subscription, path string) (string, string, error) {
	tmpDir := singBoxDataDir()
	if err := os.MkdirAll(tmpDir, 0700); err != nil {
		return "", "", err
	}
	providerTag := singBoxProviderTag(item.ID)
	headers := effectiveSubscriptionHeaders(item.Headers)
	config := map[string]interface{}{
		"providers": []interface{}{
			map[string]interface{}{
				"type":            "remote",
				"tag":             providerTag,
				"url":             strings.TrimSpace(item.URL),
				"path":            path,
				"http_client":     map[string]interface{}{"headers": headers},
				"update_interval": cfg.ProviderUpdateInterval,
			},
		},
		"outbounds": []interface{}{map[string]interface{}{"type": "direct", "tag": "direct"}},
		"route":     map[string]interface{}{"final": "direct", "auto_detect_interface": true},
	}
	configPath := filepath.Join(tmpDir, fmt.Sprintf(".singbox-provider-%d.json", time.Now().UnixNano()))
	logPath := filepath.Join(tmpDir, fmt.Sprintf(".singbox-provider-%d.log", time.Now().UnixNano()))
	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return "", "", err
	}
	if err := os.WriteFile(configPath, data, 0600); err != nil {
		return "", "", err
	}
	return configPath, logPath, nil
}

func effectiveSubscriptionHeaders(headers map[string]string) map[string]string {
	result := map[string]string{
		"User-Agent": "v2rayNG/2.2.6",
		"Connection": "close",
	}
	for key, value := range headers {
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if key != "" && value != "" {
			result[key] = value
		}
	}
	return result
}

func providerCacheLooksValid(path string) bool {
	raw, err := os.ReadFile(path)
	if err != nil || len(strings.TrimSpace(string(raw))) == 0 {
		return false
	}
	var doc struct {
		Outbounds []map[string]interface{} `json:"outbounds"`
		Endpoints []map[string]interface{} `json:"endpoints"`
	}
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.UseNumber()
	if err := dec.Decode(&doc); err != nil {
		return false
	}
	return len(doc.Outbounds) > 0 || len(doc.Endpoints) > 0
}

func restoreProviderBackup(path, backupPath string) error {
	if _, err := os.Stat(backupPath); err != nil {
		return nil
	}
	_ = os.Remove(path)
	return os.Rename(backupPath, path)
}

func loadAggregatedSingBoxNodes() ([]singBoxCachedNode, error) {
	cfg, err := loadSingBoxEngineConfig()
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(singBoxNodesDir(cfg))
	if os.IsNotExist(err) {
		return []singBoxCachedNode{}, nil
	}
	if err != nil {
		return nil, err
	}

	if err := ensureSubscriptionStoreLoaded(); err != nil {
		return nil, err
	}
	subscriptionStore.Lock()
	subMap := make(map[string]subscription, len(subscriptionStore.Items))
	for _, item := range subscriptionStore.Items {
		subMap[item.Name] = item
	}
	subscriptionStore.Unlock()

	merged := make(map[string]*singBoxCachedNode)
	order := make([]string, 0)
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") || entry.Name() == singBoxEngineConfigFile {
			continue
		}
		subName := strings.TrimSuffix(entry.Name(), ".json")
		item := subMap[subName]
		providerTag := singBoxProviderTag(item.ID)
		nodes, err := loadProviderCacheNodes(filepath.Join(singBoxNodesDir(cfg), entry.Name()), item, providerTag)
		if err != nil {
			continue
		}
		for _, node := range nodes {
			key := singBoxEndpointKey(node.Server, node.Port)
			existing := merged[key]
			if existing == nil {
				copy := node
				copy.ID = singBoxNodeID(node.Server, node.Port)
				copy.Sources = uniqueNodeSources(copy.Sources)
				if len(copy.Variants) == 0 {
					copy.Variants = []singBoxNodeVariant{{
						Protocol: copy.Protocol, Name: copy.Name, Server: copy.Server, Port: copy.Port,
						Provider: copy.Provider, OutboundTag: copy.OutboundTag, Outbound: cloneInterfaceMap(copy.Outbound), Sources: uniqueNodeSources(copy.Sources),
					}}
				}
				merged[key] = &copy
				order = append(order, key)
				continue
			}
			existing.Sources = uniqueNodeSources(append(existing.Sources, node.Sources...))
			if len(node.Variants) > 0 {
				existing.Variants = appendUniqueNodeVariants(existing.Variants, node.Variants...)
			} else {
				existing.Variants = appendUniqueNodeVariants(existing.Variants, singBoxNodeVariant{
					Protocol: node.Protocol, Name: node.Name, Server: node.Server, Port: node.Port,
					Provider: node.Provider, OutboundTag: node.OutboundTag, Outbound: cloneInterfaceMap(node.Outbound), Sources: uniqueNodeSources(node.Sources),
				})
			}
		}
	}
	result := make([]singBoxCachedNode, 0, len(order))
	for _, key := range order {
		if item := merged[key]; item != nil {
			item.Sources = uniqueNodeSources(item.Sources)
			result = append(result, *item)
		}
	}
	return result, nil
}

func loadProviderCacheNodes(path string, item subscription, providerTag string) ([]singBoxCachedNode, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var doc struct {
		Outbounds []map[string]interface{} `json:"outbounds"`
		Endpoints []map[string]interface{} `json:"endpoints"`
	}
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.UseNumber()
	if err := dec.Decode(&doc); err != nil {
		return nil, fmt.Errorf("解析 Provider 缓存失败: %w", err)
	}

	result := make([]singBoxCachedNode, 0, len(doc.Outbounds))
	for _, outbound := range doc.Outbounds {
		typeName := strings.ToLower(stringValue(outbound["type"]))
		server := stringValue(outbound["server"])
		port := intFromJSONNumber(outbound["server_port"])
		if server == "" || port <= 0 || typeName == "" {
			continue
		}
		tag := stringValue(outbound["tag"])
		if tag == "" {
			continue
		}
		name := tag
		if index := strings.LastIndex(tag, "/"); index >= 0 && index+1 < len(tag) {
			name = tag[index+1:]
		}
		rawInput := findRawEndpointForSubscription(item.Content, server, port)
		source := singBoxNodeSource{
			SubscriptionID: item.ID, SubscriptionName: item.Name, SubscriptionURL: item.URL,
			ProviderTag: providerTag, NodeTag: tag, Raw: rawInput,
		}
		node := singBoxCachedNode{
			ID: singBoxNodeID(server, port), Server: server, Port: port, Protocol: typeName,
			Name: name, Provider: providerTag, OutboundTag: tag, Outbound: cloneInterfaceMap(outbound),
			Sources: []singBoxNodeSource{source}, Variants: []singBoxNodeVariant{{
				Protocol: typeName, Name: name, Server: server, Port: port, Provider: providerTag,
				OutboundTag: tag, Outbound: cloneInterfaceMap(outbound), Sources: []singBoxNodeSource{source},
			}},
		}
		result = append(result, node)
	}
	return result, nil
}

func findRawEndpointForSubscription(content, server string, port int) string {
	content = strings.TrimSpace(strings.TrimPrefix(content, "\ufeff"))
	if content == "" {
		return ""
	}
	decoded := decodeMaybeBase64Subscription(content)
	for _, line := range strings.Split(decoded, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.Contains(line, server+":"+strconv.Itoa(port)) {
			return line
		}
	}
	return ""
}

func testCachedSingBoxNode(parent context.Context, cfg singBoxEngineConfig, node singBoxCachedNode, variantIndex, repeat int, timeout time.Duration, downloadBytes int64, downloadURL string) singBoxNodeResult {
	variants := append([]singBoxNodeVariant(nil), node.Variants...)
	if len(variants) == 0 {
		variants = []singBoxNodeVariant{{
			Protocol: node.Protocol, Name: node.Name, Server: node.Server, Port: node.Port,
			Provider: node.Provider, OutboundTag: node.OutboundTag, Outbound: cloneInterfaceMap(node.Outbound), Sources: node.Sources,
		}}
	}

	runtime, err := startSingBoxRuntime(parent, cfg, []singBoxCachedNode{node})
	if err != nil {
		name := node.Name
		protocol := node.Protocol
		if variantIndex >= 0 && variantIndex < len(variants) {
			name = variants[variantIndex].Name
			protocol = variants[variantIndex].Protocol
		}
		return singBoxNodeResult{
			NodeID: node.ID, Node: name, Protocol: protocol, Server: node.Server, Port: node.Port,
			Success: false, TotalAttempts: repeat, LossRate: 100, Mode: "unavailable", Error: err.Error(),
		}
	}
	defer runtime.Close()

	if variantIndex >= 0 && variantIndex < len(variants) {
		return testVariantInRuntime(parent, runtime, cfg, node, variants[variantIndex], repeat, timeout, downloadBytes, downloadURL)
	}

	var last singBoxNodeResult
	for _, variant := range variants {
		result := testVariantInRuntime(parent, runtime, cfg, node, variant, repeat, timeout, downloadBytes, downloadURL)
		if result.Success {
			return result
		}
		last = result
		if parent.Err() != nil {
			break
		}
	}
	if last.NodeID == "" {
		last = singBoxNodeResult{NodeID: node.ID, Node: node.Name, Protocol: node.Protocol, Server: node.Server, Port: node.Port, Mode: runtime.mode, TotalAttempts: repeat, LossRate: 100}
	}
	return last
}

func startSingBoxRuntime(parent context.Context, cfg singBoxEngineConfig, nodes []singBoxCachedNode) (*singBoxRuntime, error) {
	binary, err := findSingBoxBinary()
	if err != nil {
		return nil, err
	}
	if len(nodes) == 0 {
		return nil, errors.New("没有可用于启动 sing-box 的节点")
	}
	providers := providerRefsForNodes(cfg, nodes)
	if len(providers) == 0 {
		return nil, errors.New("没有可用的 Provider 缓存")
	}

	controllerPort, err := reserveLocalPort()
	if err != nil {
		return nil, err
	}
	secret, err := randomControllerSecret()
	if err != nil {
		return nil, err
	}

	tmpDir := singBoxDataDir()
	configPath := filepath.Join(tmpDir, fmt.Sprintf(".singbox-test-%d.json", time.Now().UnixNano()))
	logPath := filepath.Join(tmpDir, fmt.Sprintf(".singbox-test-%d.log", time.Now().UnixNano()))

	outbounds := []interface{}{map[string]interface{}{"type": "direct", "tag": "direct"}}
	if len(providers) > 0 {
		outbounds = append(outbounds, map[string]interface{}{
			"type":                        "selector",
			"tag":                         singBoxSelectorTag,
			"providers":                   providers,
			"default":                     "direct",
			"interrupt_exist_connections": true,
		})
	}
	config := map[string]interface{}{
		"providers": providerConfigObjects(providers, cfg),
		"outbounds": outbounds,
		"experimental": map[string]interface{}{
			"clash_api": map[string]interface{}{
				"external_controller": fmt.Sprintf("%s:%d", cfg.ControllerHost, controllerPort),
				"secret":              secret,
			},
		},
		"route": map[string]interface{}{
			"final":                 "direct",
			"auto_detect_interface": true,
		},
	}

	if cfg.PreferSingTun {
		if appUID, ok := configuredAppUID(); ok {
			config["inbounds"] = []interface{}{map[string]interface{}{
				"type":           "tun",
				"tag":            singBoxTunTag,
				"interface_name": fmt.Sprintf("cfdtun-%d", controllerPort%10000),
				"address":        []string{"172.19.0.1/30"},
				"mtu":            1500,
				"auto_route":     true,
				"strict_route":   true,
				"include_uid":    []int{appUID},
			}}
			config["route"].(map[string]interface{})["final"] = singBoxSelectorTag
		}
	}

	fallbackProxyPort := 0
	if !cfg.PreferSingTun || !cfg.SingTunRequiresRoot {
		fallbackProxyPort, err = reserveLocalPort()
		if err != nil {
			return nil, err
		}
		config["inbounds"] = []interface{}{map[string]interface{}{
			"type": "mixed", "tag": singBoxMixedTag, "listen": cfg.LocalProxyHost, "listen_port": fallbackProxyPort,
		}}
		config["route"].(map[string]interface{})["final"] = singBoxSelectorTag
	}

	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(configPath, data, 0600); err != nil {
		return nil, err
	}
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		_ = os.Remove(configPath)
		return nil, err
	}

	cmd, mode, err := buildSingBoxRunCommand(parent, binary, configPath, logFile, cfg.PreferSingTun && cfg.SingTunRequiresRoot)
	if err != nil {
		_ = logFile.Close()
		_ = os.Remove(configPath)
		_ = os.Remove(logPath)
		if cfg.MixedFallback {
			return startSingBoxMixedFallback(parent, cfg, nodes)
		}
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		logFile.Close()
		_ = os.Remove(configPath)
		_ = os.Remove(logPath)
		return nil, fmt.Errorf("启动 sing-box 失败: %w", err)
	}

	runtime := &singBoxRuntime{
		process: cmd, configPath: configPath, logPath: logPath, mode: mode,
		controllerURL: fmt.Sprintf("http://%s:%d", cfg.ControllerHost, controllerPort),
		secret:        secret,
	}
	if fallbackProxyPort > 0 {
		runtime.proxyURL = fmt.Sprintf("http://%s:%d", cfg.LocalProxyHost, fallbackProxyPort)
	}

	readyCtx, cancel := context.WithTimeout(parent, 15*time.Second)
	defer cancel()
	if err := waitForClashAPI(readyCtx, runtime.controllerURL, runtime.secret); err != nil {
		runtime.Close()
		if fallback, fallbackErr := startSingBoxMixedFallback(parent, cfg, nodes); fallbackErr == nil {
			return fallback, nil
		}
		return nil, fmt.Errorf("sing-box %s 启动失败: %w", mode, err)
	}
	return runtime, nil
}

func startSingBoxMixedFallback(parent context.Context, cfg singBoxEngineConfig, nodes []singBoxCachedNode) (*singBoxRuntime, error) {
	if !cfg.MixedFallback {
		return nil, errors.New("未启用 mixed fallback")
	}
	binary, err := findSingBoxBinary()
	if err != nil {
		return nil, err
	}
	providers := providerRefsForNodes(cfg, nodes)
	controllerPort, err := reserveLocalPort()
	if err != nil {
		return nil, err
	}
	proxyPort, err := reserveLocalPort()
	if err != nil {
		return nil, err
	}
	secret, err := randomControllerSecret()
	if err != nil {
		return nil, err
	}
	config := map[string]interface{}{
		"providers": providerConfigObjects(providers, cfg),
		"inbounds": []interface{}{map[string]interface{}{
			"type": "mixed", "tag": singBoxMixedTag, "listen": cfg.LocalProxyHost, "listen_port": proxyPort,
		}},
		"outbounds": []interface{}{map[string]interface{}{"type": "direct", "tag": "direct"}, map[string]interface{}{
			"type": "selector", "tag": singBoxSelectorTag, "providers": providers, "default": "direct", "interrupt_exist_connections": true,
		}},
		"experimental": map[string]interface{}{"clash_api": map[string]interface{}{
			"external_controller": fmt.Sprintf("%s:%d", cfg.ControllerHost, controllerPort), "secret": secret,
		}},
		"route": map[string]interface{}{"final": singBoxSelectorTag, "auto_detect_interface": true},
	}
	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return nil, err
	}
	configPath := filepath.Join(singBoxDataDir(), fmt.Sprintf(".singbox-mixed-%d.json", time.Now().UnixNano()))
	logPath := filepath.Join(singBoxDataDir(), fmt.Sprintf(".singbox-mixed-%d.log", time.Now().UnixNano()))
	if err := os.WriteFile(configPath, data, 0600); err != nil {
		return nil, err
	}
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		_ = os.Remove(configPath)
		return nil, err
	}
	cmd := exec.CommandContext(parent, binary, "run", "-c", configPath)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	if err := cmd.Start(); err != nil {
		logFile.Close()
		_ = os.Remove(configPath)
		_ = os.Remove(logPath)
		return nil, err
	}
	runtime := &singBoxRuntime{
		process: cmd, configPath: configPath, logPath: logPath, mode: "mixed",
		controllerURL: fmt.Sprintf("http://%s:%d", cfg.ControllerHost, controllerPort),
		secret:        secret, proxyURL: fmt.Sprintf("http://%s:%d", cfg.LocalProxyHost, proxyPort),
	}
	ctx, cancel := context.WithTimeout(parent, 15*time.Second)
	defer cancel()
	if err := waitForClashAPI(ctx, runtime.controllerURL, runtime.secret); err != nil {
		runtime.Close()
		return nil, fmt.Errorf("mixed fallback 启动失败: %w", err)
	}
	return runtime, nil
}

func buildSingBoxRunCommand(parent context.Context, binary, configPath string, logFile *os.File, preferRoot bool) (*exec.Cmd, string, error) {
	if preferRoot {
		suPath := findSuBinary()
		if suPath == "" {
			return nil, "sing-tun", errors.New("未找到 su；sing-tun TUN 需要 root，mixed fallback 将在外层处理")
		}
		if !canUseRootSu(suPath) {
			return nil, "sing-tun", errors.New("当前 su 无法获得 root 权限")
		}
		command := shellQuote(binary) + " run -c " + shellQuote(configPath)
		cmd := exec.CommandContext(parent, suPath, "-c", "exec "+command)
		cmd.Stdout = logFile
		cmd.Stderr = logFile
		return cmd, "sing-tun", nil
	}
	cmd := exec.CommandContext(parent, binary, "run", "-c", configPath)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	return cmd, "mixed", nil
}

func testNodeInRuntime(parent context.Context, runtime *singBoxRuntime, cfg singBoxEngineConfig, node singBoxCachedNode, repeat int, timeout time.Duration, downloadBytes int64, downloadURL string) singBoxNodeResult {
	variants := append([]singBoxNodeVariant(nil), node.Variants...)
	if len(variants) == 0 {
		variants = []singBoxNodeVariant{{
			Protocol: node.Protocol, Name: node.Name, Server: node.Server, Port: node.Port,
			Provider: node.Provider, OutboundTag: node.OutboundTag, Outbound: cloneInterfaceMap(node.Outbound), Sources: node.Sources,
		}}
	}
	var last singBoxNodeResult
	for _, variant := range variants {
		result := testVariantInRuntime(parent, runtime, cfg, node, variant, repeat, timeout, downloadBytes, downloadURL)
		if result.Success {
			return result
		}
		last = result
		if parent.Err() != nil {
			break
		}
	}
	return last
}

func testVariantInRuntime(parent context.Context, runtime *singBoxRuntime, cfg singBoxEngineConfig, node singBoxCachedNode, variant singBoxNodeVariant, repeat int, timeout time.Duration, downloadBytes int64, downloadURL string) singBoxNodeResult {
	result := singBoxNodeResult{
		NodeID: node.ID, Node: variant.Name, Protocol: variant.Protocol, Server: variant.Server, Port: variant.Port,
		TotalAttempts: repeat, Mode: runtime.mode,
	}
	if len(variant.OutboundTag) == 0 {
		variant.OutboundTag = node.OutboundTag
	}
	if err := selectSingBoxOutbound(parent, runtime, variant.OutboundTag); err != nil {
		result.LossRate = 100
		result.Error = "选择节点失败: " + err.Error()
		return result
	}

	testURL := "https://speed.cloudflare.com/cdn-cgi/trace"
	var resolved []net.IP
	if runtime.mode == "sing-tun" {
		var err error
		resolved, err = resolveHostIPs(parent, testURL)
		if err != nil {
			result.LossRate = 100
			result.Error = "测试地址解析失败: " + err.Error()
			return result
		}
	}
	client := newSingBoxHTTPClient(timeout, resolved, runtime.mode == "mixed", runtime.proxyURL)
	results := make([]singBoxProbeResult, 0, repeat)
	var latencySum int64
	var minLatency, maxLatency int64
	var firstIP string
	for attempt := 1; attempt <= repeat; attempt++ {
		probe := probeThroughSingBox(client, testURL, resolved, attempt, timeout)
		results = append(results, probe)
		if probe.Success {
			result.SuccessCount++
			latencySum += probe.LatencyMS
			if minLatency == 0 || probe.LatencyMS < minLatency {
				minLatency = probe.LatencyMS
			}
			if probe.LatencyMS > maxLatency {
				maxLatency = probe.LatencyMS
			}
			if firstIP == "" {
				firstIP = probe.OutboundIP
			}
		}
	}
	result.Results = results
	if result.SuccessCount > 0 {
		result.Success = true
		result.MinLatencyMS = minLatency
		result.MaxLatencyMS = maxLatency
		result.AvgLatencyMS = float64(latencySum) / float64(result.SuccessCount)
		result.OutboundIP = firstIP
	}
	result.LossRate = float64(repeat-result.SuccessCount) * 100 / float64(maxInt(1, repeat))
	result.WorkingSource = firstSourceName(variant.Sources)

	if result.Success && downloadBytes > 0 && strings.TrimSpace(downloadURL) != "" {
		resolvedDownload := []net.IP(nil)
		if runtime.mode == "sing-tun" {
			resolvedDownload, _ = resolveHostIPs(parent, downloadURL)
		}
		downloadClient := client
		if runtime.mode == "sing-tun" && len(resolvedDownload) > 0 {
			downloadClient = newSingBoxHTTPClient(timeout, resolvedDownload, false, "")
		}
		if speed := measureSingBoxDownload(downloadClient, downloadURL, resolvedDownload, downloadBytes, timeout); speed != "" {
			result.Speed = speed
		}
	}
	if !result.Success && len(results) > 0 {
		result.Error = firstProbeError(results)
	}
	return result
}

func selectSingBoxOutbound(parent context.Context, runtime *singBoxRuntime, outboundTag string) error {
	outboundTag = strings.TrimSpace(outboundTag)
	if outboundTag == "" {
		return errors.New("节点标签为空")
	}
	body, _ := json.Marshal(map[string]string{"name": outboundTag})
	req, err := http.NewRequestWithContext(parent, http.MethodPut, runtime.controllerURL+"/proxies/"+url.PathEscape(singBoxSelectorTag), strings.NewReader(string(body)))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+runtime.secret)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}
	return nil
}

func waitForClashAPI(ctx context.Context, controllerURL, secret string) error {
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, controllerURL+"/proxies", nil)
		if err == nil {
			req.Header.Set("Authorization", "Bearer "+secret)
			resp, callErr := http.DefaultClient.Do(req)
			if callErr == nil {
				io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
				resp.Body.Close()
				if resp.StatusCode >= 200 && resp.StatusCode < 300 {
					return nil
				}
			}
		}
		time.Sleep(150 * time.Millisecond)
	}
}

func newSingBoxHTTPClient(timeout time.Duration, resolved []net.IP, useMixed bool, proxyAddress string) *http.Client {
	transport := &http.Transport{
		Proxy:                 nil,
		ForceAttemptHTTP2:     true,
		DisableKeepAlives:     true,
		DialContext:           dialResolved(resolved),
		TLSHandshakeTimeout:   timeout,
		ResponseHeaderTimeout: timeout,
		IdleConnTimeout:       10 * time.Second,
	}
	if useMixed && proxyAddress != "" {
		if proxy, err := url.Parse(proxyAddress); err == nil {
			transport.Proxy = http.ProxyURL(proxy)
			transport.DialContext = (&net.Dialer{Timeout: timeout, KeepAlive: 30 * time.Second}).DialContext
		}
	}
	return &http.Client{Transport: transport, Timeout: timeout}
}

func dialResolved(resolved []net.IP) func(context.Context, string, string) (net.Conn, error) {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		port := "443"
		if _, value, err := net.SplitHostPort(addr); err == nil && value != "" {
			port = value
		}
		dialer := &net.Dialer{Timeout: 8 * time.Second, KeepAlive: 30 * time.Second}
		var lastErr error
		for _, ip := range resolved {
			conn, err := dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
			if err == nil {
				return conn, nil
			}
			lastErr = err
		}
		if lastErr == nil {
			lastErr = errors.New("没有解析到 IP")
		}
		return nil, lastErr
	}
}

func resolveHostIPs(parent context.Context, target string) ([]net.IP, error) {
	u, err := url.Parse(strings.TrimSpace(target))
	if err != nil || u.Hostname() == "" {
		return nil, errors.New("URL 无效")
	}
	if ip := net.ParseIP(u.Hostname()); ip != nil {
		return []net.IP{ip}, nil
	}
	ctx, cancel := context.WithTimeout(parent, 4*time.Second)
	defer cancel()
	ips, err := net.DefaultResolver.LookupIP(ctx, "ip", u.Hostname())
	if err != nil {
		return nil, err
	}
	if len(ips) == 0 {
		return nil, errors.New("DNS 无结果")
	}
	return ips, nil
}

func probeThroughSingBox(client *http.Client, testURL string, resolved []net.IP, attempt int, timeout time.Duration) singBoxProbeResult {
	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, testURL, nil)
	if err != nil {
		return singBoxProbeResult{Attempt: attempt, Error: err.Error()}
	}
	resp, err := client.Do(req)
	if err != nil {
		return singBoxProbeResult{Attempt: attempt, LatencyMS: time.Since(start).Milliseconds(), Error: err.Error()}
	}
	defer resp.Body.Close()
	body, readErr := io.ReadAll(io.LimitReader(resp.Body, 128<<10))
	latency := time.Since(start).Milliseconds()
	probe := singBoxProbeResult{Attempt: attempt, LatencyMS: latency, StatusCode: resp.StatusCode}
	if resp.StatusCode >= 400 {
		probe.Error = fmt.Sprintf("HTTP %d", resp.StatusCode)
		return probe
	}
	if readErr != nil {
		probe.Error = readErr.Error()
		return probe
	}
	probe.Success = true
	probe.OutboundIP = parseTraceIP(string(body))
	_ = resolved
	return probe
}

func measureSingBoxDownload(client *http.Client, testURL string, resolved []net.IP, bytesWanted int64, timeout time.Duration) string {
	_ = resolved
	ctx, cancel := context.WithTimeout(context.Background(), timeout*2)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, testURL, nil)
	if err != nil {
		return ""
	}
	resp, err := client.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return ""
	}
	start := time.Now()
	read, err := io.CopyN(io.Discard, resp.Body, bytesWanted)
	if err != nil && read == 0 {
		return ""
	}
	elapsed := time.Since(start).Seconds()
	if elapsed <= 0 || read <= 0 {
		return ""
	}
	return fmt.Sprintf("%.2f MB/s", float64(read)/1024.0/1024.0/elapsed)
}

func parseTraceIP(content string) string {
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "ip=") {
			return strings.TrimSpace(strings.TrimPrefix(line, "ip="))
		}
	}
	return ""
}

func providerRefsForNodes(cfg singBoxEngineConfig, nodes []singBoxCachedNode) []string {
	set := make(map[string]struct{})
	for _, node := range nodes {
		if node.Provider != "" {
			set[node.Provider] = struct{}{}
		}
		for _, variant := range node.Variants {
			if variant.Provider != "" {
				set[variant.Provider] = struct{}{}
			}
		}
		for _, source := range node.Sources {
			if source.SubscriptionID != "" {
				set[singBoxProviderTag(source.SubscriptionID)] = struct{}{}
			}
		}
	}
	result := make([]string, 0, len(set))
	for tag := range set {
		if providerCacheForTagExists(cfg, tag) {
			result = append(result, tag)
		}
	}
	return result
}

func providerCacheForTagExists(cfg singBoxEngineConfig, providerTag string) bool {
	if strings.HasPrefix(providerTag, "sub-") {
		return true
	}
	return false
}

func providerConfigObjects(providerTags []string, cfg singBoxEngineConfig) []interface{} {
	result := make([]interface{}, 0, len(providerTags))
	for _, providerTag := range providerTags {
		path := ""
		subscriptionID := strings.TrimPrefix(providerTag, "sub-")
		_ = subscriptionID
		if item := subscriptionByProviderTag(providerTag); item.ID != "" {
			if filePath, err := singBoxNodeFilePath(cfg, item.Name); err == nil {
				path = filePath
			}
		}
		if path == "" {
			continue
		}
		result = append(result, map[string]interface{}{
			"type": "local", "tag": providerTag, "path": path,
		})
	}
	return result
}

func subscriptionByProviderTag(providerTag string) subscription {
	if err := ensureSubscriptionStoreLoaded(); err != nil {
		return subscription{}
	}
	subscriptionStore.Lock()
	defer subscriptionStore.Unlock()
	for _, item := range subscriptionStore.Items {
		if singBoxProviderTag(item.ID) == providerTag {
			return item
		}
	}
	return subscription{}
}

func singBoxProviderTag(subscriptionID string) string {
	h := sha256.Sum256([]byte(strings.TrimSpace(subscriptionID)))
	return "sub-" + hex.EncodeToString(h[:6])
}

func singBoxEndpointKey(server string, port int) string {
	return strings.ToLower(strings.TrimSpace(server)) + ":" + strconv.Itoa(port)
}

func singBoxNodeID(server string, port int) string {
	h := sha256.Sum256([]byte(singBoxEndpointKey(server, port)))
	return "node-" + hex.EncodeToString(h[:8])
}

func cloneInterfaceMap(value map[string]interface{}) map[string]interface{} {
	if len(value) == 0 {
		return nil
	}
	data, err := json.Marshal(value)
	if err != nil {
		return nil
	}
	var copy map[string]interface{}
	if err := json.Unmarshal(data, &copy); err != nil {
		return nil
	}
	return copy
}

func uniqueNodeSources(input []singBoxNodeSource) []singBoxNodeSource {
	seen := map[string]struct{}{}
	result := make([]singBoxNodeSource, 0, len(input))
	for _, source := range input {
		key := source.SubscriptionID + "\x00" + source.NodeTag
		if key == "\x00" {
			key = source.SubscriptionName + "\x00" + source.SubscriptionURL
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, source)
	}
	return result
}

func appendUniqueNodeVariants(base []singBoxNodeVariant, additions ...singBoxNodeVariant) []singBoxNodeVariant {
	seen := make(map[string]struct{}, len(base))
	for _, variant := range base {
		seen[variant.Provider+"\x00"+variant.OutboundTag] = struct{}{}
	}
	for _, variant := range additions {
		key := variant.Provider + "\x00" + variant.OutboundTag
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		base = append(base, variant)
	}
	return base
}

func firstSourceName(sources []singBoxNodeSource) string {
	for _, source := range sources {
		if strings.TrimSpace(source.SubscriptionName) != "" {
			return source.SubscriptionName
		}
	}
	return ""
}

func firstProbeError(results []singBoxProbeResult) string {
	for _, item := range results {
		if strings.TrimSpace(item.Error) != "" {
			return item.Error
		}
	}
	return "节点测试失败"
}

func intFromJSONNumber(value interface{}) int {
	switch typed := value.(type) {
	case json.Number:
		v, _ := typed.Int64()
		return int(v)
	case float64:
		return int(typed)
	case int:
		return typed
	case int64:
		return int(typed)
	case string:
		v, _ := strconv.Atoi(strings.TrimSpace(typed))
		return v
	default:
		return 0
	}
}

func stringValue(value interface{}) string {
	if value == nil {
		return ""
	}
	return strings.TrimSpace(fmt.Sprint(value))
}

func configuredAppUID() (int, bool) {
	raw := strings.TrimSpace(os.Getenv("CFDATA_APP_UID"))
	if raw == "" {
		return 0, false
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 0 {
		return 0, false
	}
	return value, true
}

func findSingBoxBinary() (string, error) {
	candidates := []string{}
	if value := strings.TrimSpace(os.Getenv("CFDATA_SINGBOX_PATH")); value != "" {
		candidates = append(candidates, value)
	}
	if value, err := exec.LookPath("sing-box"); err == nil {
		candidates = append(candidates, value)
	}
	candidates = append(candidates, filepath.Join(singBoxDataDir(), "sing-box"), filepath.Join(filepath.Dir(os.Args[0]), "sing-box"))
	seen := map[string]struct{}{}
	for _, value := range candidates {
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		if info, err := os.Stat(value); err == nil && !info.IsDir() {
			return value, nil
		}
	}
	return "", errors.New("未找到 sing-box 核心，请确认 APK 已内置 Android ARM64 reF1nd sing-box")
}

func findSuBinary() string {
	for _, path := range []string{"/system/bin/su", "/system/xbin/su", "/sbin/su"} {
		if info, err := os.Stat(path); err == nil && !info.IsDir() {
			return path
		}
	}
	if path, err := exec.LookPath("su"); err == nil {
		return path
	}
	return ""
}

func canUseRootSu(suPath string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, suPath, "-c", "id").CombinedOutput()
	if err != nil {
		return false
	}
	return strings.Contains(string(out), "uid=0")
}

func reserveLocalPort() (int, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		return 0, err
	}
	return port, nil
}

func randomControllerSecret() (string, error) {
	buf := make([]byte, singBoxControllerSecretBytes)
	for i := range buf {
		buf[i] = byte(time.Now().UnixNano()>>uint((i%8)*8)) ^ byte(i*31+17)
	}
	h := sha256.Sum256(buf)
	return hex.EncodeToString(h[:12]), nil
}

func terminateProcess(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	_ = cmd.Process.Signal(os.Interrupt)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cmd.ProcessState != nil && cmd.ProcessState.Exited() {
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	_ = cmd.Process.Kill()
	return nil
}

func (runtime *singBoxRuntime) Close() {
	if runtime == nil {
		return
	}
	runtime.closeOnce.Do(func() {
		_ = terminateProcess(runtime.process)
		_ = os.Remove(runtime.configPath)
		_ = os.Remove(runtime.logPath)
	})
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

func lastLogLines(text string, maxLines int) string {
	lines := strings.Split(text, "\n")
	if len(lines) <= maxLines {
		return strings.Join(lines, " | ")
	}
	return strings.Join(lines[len(lines)-maxLines:], " | ")
}

func decodeMaybeBase64Subscription(content string) string {
	content = strings.TrimSpace(strings.TrimPrefix(content, "\ufeff"))
	compact := strings.Map(func(r rune) rune {
		switch r {
		case ' ', '\t', '\r', '\n':
			return -1
		default:
			return r
		}
	}, content)
	if decoded, err := decodeBase64URL(compact); err == nil {
		decodedText := strings.TrimSpace(decoded)
		if strings.Contains(decodedText, "://") || strings.HasPrefix(decodedText, "{") || strings.HasPrefix(decodedText, "[") {
			return decodedText
		}
	}
	return content
}

func decodeBase64URL(value string) (string, error) {
	if decoded, err := base64DecodeStd(value); err == nil {
		return decoded, nil
	}
	padding := len(value) % 4
	if padding != 0 {
		value += strings.Repeat("=", 4-padding)
	}
	if decoded, err := base64DecodeURLSafe(value); err == nil {
		return decoded, nil
	}
	return "", errors.New("base64 decode failed")
}

func base64DecodeStd(value string) (string, error) {
	decoded, err := base64.StdEncoding.DecodeString(value)
	if err != nil {
		return "", err
	}
	return string(decoded), nil
}

func base64DecodeURLSafe(value string) (string, error) {
	decoded, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(value, "="))
	if err != nil {
		return "", err
	}
	return string(decoded), nil
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func writeSingBoxJSON(w http.ResponseWriter, status int, value interface{}) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
