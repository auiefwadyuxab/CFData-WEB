package daemon

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"strconv"
	"strings"
	"time"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/common/dialer"
	E "github.com/sagernet/sing/common/exceptions"
	"github.com/sagernet/sing/common/metadata"
)

type cfDataTraceAttempt struct {
	Attempt        int    `json:"attempt"`
	Success        bool   `json:"success"`
	LatencyMs      int64  `json:"latencyMs"`
	StatusCode     int    `json:"statusCode,omitempty"`
	OutboundIP     string `json:"outboundIP,omitempty"`
	Error          string `json:"error,omitempty"`
	TCPConnectMs   int64  `json:"tcpConnectMs,omitempty"`
	TLSHandshakeMs int64  `json:"tlsHandshakeMs,omitempty"`
	TTFBMs         int64  `json:"ttfbMs,omitempty"`
}

type cfDataTrueTestResult struct {
	Success       bool                 `json:"success"`
	SuccessCount  int                  `json:"successCount"`
	TotalAttempts int                  `json:"totalAttempts"`
	LossRate      float64              `json:"lossRate"`
	AvgLatencyMs  float64              `json:"avgLatencyMs,omitempty"`
	MinLatencyMs  int64                `json:"minLatencyMs,omitempty"`
	MaxLatencyMs  int64                `json:"maxLatencyMs,omitempty"`
	OutboundIP    string               `json:"outboundIP,omitempty"`
	Speed         string               `json:"speed,omitempty"`
	Results       []cfDataTraceAttempt `json:"results"`
	Error         string               `json:"error,omitempty"`
	Mode          string               `json:"mode"`
}

// CFDataTrueTest runs the CFData trace/download checks through one already
// running sing-box outbound. It intentionally bypasses mixed/HTTP proxy
// inbounds: the HTTP transport's DialContext is wired directly to the
// outbound's ResolveDialer, matching SFA's outbound test model.
func (s *StartedService) CFDataTrueTest(
	outboundTag string,
	testURL string,
	downloadURL string,
	repeat int32,
	timeoutSeconds int32,
	downloadBytes int64,
) (string, error) {
	if outboundTag == "" {
		return "", E.New("outbound tag is empty")
	}
	if testURL == "" {
		return "", E.New("test URL is empty")
	}
	if repeat <= 0 {
		repeat = 1
	}
	if repeat > 10 {
		repeat = 10
	}
	if timeoutSeconds <= 0 {
		timeoutSeconds = 8
	}
	if timeoutSeconds > 60 {
		timeoutSeconds = 60
	}
	if downloadBytes < 0 {
		downloadBytes = 0
	}

	ctx, cancel := context.WithCancel(s.ctx)
	defer cancel()
	if err := s.waitForStarted(ctx); err != nil {
		return "", E.Cause(err, "wait for sing-box started")
	}

	s.serviceAccess.RLock()
	instance := s.instance
	s.serviceAccess.RUnlock()
	if instance == nil {
		return "", E.New("sing-box instance is not running")
	}

	outbound, err := resolveOutbound(instance, outboundTag)
	if err != nil {
		return "", err
	}

	resolvedDialer := dialer.NewResolveDialer(
		instance.ctx,
		outbound,
		true,
		"",
		adapter.DNSQueryOptions{},
		0,
	)

	transport := &http.Transport{
		Proxy:               nil,
		ForceAttemptHTTP2:   true,
		DisableKeepAlives:   true,
		TLSHandshakeTimeout: time.Duration(timeoutSeconds) * time.Second,
		DialContext: func(ctx context.Context, networkName, address string) (net.Conn, error) {
			return resolvedDialer.DialContext(ctx, networkName, metadata.ParseSocksaddr(address))
		},
	}
	client := &http.Client{Transport: transport}
	defer transport.CloseIdleConnections()

	result := cfDataTrueTestResult{
		TotalAttempts: int(repeat),
		Results:       make([]cfDataTraceAttempt, 0, repeat),
		Mode:          "libbox-in-process-direct-outbound",
	}

	var latencyTotal int64
	for attempt := 1; attempt <= int(repeat); attempt++ {
		probe := runCFDataTraceAttempt(ctx, client, testURL, time.Duration(timeoutSeconds)*time.Second, attempt)
		result.Results = append(result.Results, probe)
		if probe.Success {
			result.SuccessCount++
			latencyTotal += probe.LatencyMs
			if result.MinLatencyMs == 0 || probe.LatencyMs < result.MinLatencyMs {
				result.MinLatencyMs = probe.LatencyMs
			}
			if probe.LatencyMs > result.MaxLatencyMs {
				result.MaxLatencyMs = probe.LatencyMs
			}
			if result.OutboundIP == "" {
				result.OutboundIP = probe.OutboundIP
			}
		}
	}

	result.LossRate = float64(int(repeat)-result.SuccessCount) * 100 / float64(repeat)
	result.Success = result.SuccessCount > 0
	if result.SuccessCount > 0 {
		result.AvgLatencyMs = float64(latencyTotal) / float64(result.SuccessCount)
	}
	if !result.Success {
		for _, probe := range result.Results {
			if probe.Error != "" {
				result.Error = probe.Error
				break
			}
		}
		if result.Error == "" {
			result.Error = "all trace attempts failed"
		}
	}

	if result.Success && downloadBytes > 0 && downloadURL != "" {
		result.Speed = runCFDataDownload(client, downloadURL, downloadBytes, time.Duration(timeoutSeconds)*time.Second)
	}

	data, err := json.Marshal(result)
	if err != nil {
		return "", E.Cause(err, "marshal CFData test result")
	}
	return string(data), nil
}

func runCFDataTraceAttempt(
	parent context.Context,
	client *http.Client,
	rawURL string,
	timeout time.Duration,
	attempt int,
) cfDataTraceAttempt {
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()

	var connectStart, connectDone, tlsStart, tlsDone, firstResponseByte time.Time
	started := time.Now()
	trace := &httptrace.ClientTrace{
		ConnectStart: func(_, _ string) {
			if connectStart.IsZero() {
				connectStart = time.Now()
			}
		},
		ConnectDone: func(_, _ string, _ error) {
			if connectDone.IsZero() {
				connectDone = time.Now()
			}
		},
		TLSHandshakeStart: func() {
			if tlsStart.IsZero() {
				tlsStart = time.Now()
			}
		},
		TLSHandshakeDone: func(_ tls.ConnectionState, _ error) {
			if tlsDone.IsZero() {
				tlsDone = time.Now()
			}
		},
		GotFirstResponseByte: func() {
			if firstResponseByte.IsZero() {
				firstResponseByte = time.Now()
			}
		},
	}

	result := cfDataTraceAttempt{Attempt: attempt}
	req, err := http.NewRequestWithContext(httptrace.WithClientTrace(ctx, trace), http.MethodGet, rawURL, nil)
	if err != nil {
		result.Error = err.Error()
		result.LatencyMs = time.Since(started).Milliseconds()
		return result
	}
	req.Close = true
	req.Header.Set("Accept-Encoding", "identity")
	req.Header.Set("Connection", "close")

	resp, err := client.Do(req)
	if err != nil {
		result.Error = err.Error()
		result.LatencyMs = time.Since(started).Milliseconds()
		return result
	}
	defer resp.Body.Close()

	result.StatusCode = resp.StatusCode
	body, err := io.ReadAll(resp.Body)
	end := time.Now()
	if err != nil {
		result.Error = err.Error()
		result.LatencyMs = end.Sub(started).Milliseconds()
		return result
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		result.Error = "unexpected HTTP status: " + resp.Status
		result.LatencyMs = end.Sub(started).Milliseconds()
		return result
	}

	if !firstResponseByte.IsZero() {
		result.TTFBMs = firstResponseByte.Sub(started).Milliseconds()
		result.LatencyMs = result.TTFBMs
	} else {
		result.LatencyMs = end.Sub(started).Milliseconds()
	}
	if !connectStart.IsZero() && !connectDone.IsZero() && connectDone.After(connectStart) {
		result.TCPConnectMs = connectDone.Sub(connectStart).Milliseconds()
	}
	if !tlsStart.IsZero() && !tlsDone.IsZero() && tlsDone.After(tlsStart) {
		result.TLSHandshakeMs = tlsDone.Sub(tlsStart).Milliseconds()
	}
	result.OutboundIP = parseCFDataTraceIP(string(body))
	result.Success = true
	return result
}

func runCFDataDownload(
	client *http.Client,
	rawURL string,
	bytesWanted int64,
	timeout time.Duration,
) string {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return ""
	}
	req.Close = true
	req.Header.Set("Accept-Encoding", "identity")
	req.Header.Set("Connection", "close")

	resp, err := client.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return ""
	}

	start := time.Now()
	var total int64
	buffer := make([]byte, 128*1024)
	for total < bytesWanted {
		wanted := int64(len(buffer))
		if remain := bytesWanted - total; remain < wanted {
			wanted = remain
		}
		n, readErr := resp.Body.Read(buffer[:wanted])
		if n > 0 {
			total += int64(n)
		}
		if readErr != nil {
			if readErr == io.EOF {
				break
			}
			return ""
		}
	}
	elapsed := time.Since(start).Seconds()
	if total <= 0 || elapsed <= 0 {
		return ""
	}
	mibPerSecond := float64(total) / (1024 * 1024) / elapsed
	return formatSpeed(mibPerSecond)
}

func parseCFDataTraceIP(text string) string {
	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)
		if len(trimmed) > 3 && trimmed[:3] == "ip=" {
			return strings.TrimSpace(trimmed[3:])
		}
	}
	return ""
}

func formatSpeed(mibPerSecond float64) string {
	return formatFloatTwo(mibPerSecond) + " MB/s"
}

func formatFloatTwo(value float64) string {
	return strconv.FormatFloat(value, 'f', 2, 64)
}
