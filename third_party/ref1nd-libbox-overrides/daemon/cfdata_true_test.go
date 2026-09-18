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
	"sync"
	"time"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/common/dialer"
	E "github.com/sagernet/sing/common/exceptions"
	"github.com/sagernet/sing/common/metadata"
	"github.com/sagernet/sing/common/ntp"
)

type cfDataTraceAttempt struct {
	Attempt        int    `json:"attempt"`
	Success        bool   `json:"success"`
	LatencyMs      int64  `json:"latencyMs"`
	StatusCode     int    `json:"statusCode,omitempty"`
	OutboundIP     string `json:"outboundIP,omitempty"`
	Colo           string `json:"colo,omitempty"`
	Error          string `json:"error,omitempty"`
	OutboundDialMs int64  `json:"outboundDialMs,omitempty"`
	TLSHandshakeMs int64  `json:"tlsHandshakeMs,omitempty"`
	TTFBMs         int64  `json:"ttfbMs,omitempty"`
}

type cfDataTrueLatencyResult struct {
	Success       bool                 `json:"success"`
	SuccessCount  int                  `json:"successCount"`
	TotalAttempts int                  `json:"totalAttempts"`
	LossRate      float64              `json:"lossRate"`
	AvgLatencyMs  float64              `json:"avgLatencyMs"`
	MinLatencyMs  int64                `json:"minLatencyMs,omitempty"`
	MaxLatencyMs  int64                `json:"maxLatencyMs,omitempty"`
	OutboundIP    string               `json:"outboundIP,omitempty"`
	Colo          string               `json:"colo,omitempty"`
	Error         string               `json:"error,omitempty"`
	Results       []cfDataTraceAttempt `json:"results"`
	Mode          string               `json:"mode"`
}

type cfDataTrueSpeedResult struct {
	Success    bool    `json:"success"`
	Speed      string  `json:"speed,omitempty"`
	SpeedMBps  float64 `json:"speedMBps,omitempty"`
	DurationMs int64   `json:"speedDurationMs,omitempty"`
	Bytes      int64   `json:"speedBytes,omitempty"`
	StatusCode int     `json:"statusCode,omitempty"`
	OutboundIP string  `json:"outboundIP,omitempty"`
	Error      string  `json:"error,omitempty"`
	Mode       string  `json:"mode"`
}

const (
	cfDataDefaultLatencyRepeat  = 3
	cfDataDefaultLatencyTimeout = 3 * time.Second
	cfDataDefaultSpeedDuration  = 6 * time.Second
	cfDataMaxLatencyRepeat      = 10
	cfDataMaxLatencyTimeout     = 60 * time.Second
	cfDataMaxSpeedDuration      = 120 * time.Second
	cfDataDefaultTraceURL       = "https://speed.cloudflare.com/cdn-cgi/trace"
)

// CFDataTrueLatencyTest performs real HTTP probes through the exact sing-box
// outbound selected by the caller. This deliberately does not use TCPing,
// HTTPing multipliers, a local mixed inbound, or a standalone sing-box process.
func (s *StartedService) CFDataTrueLatencyTest(
	outboundTag string,
	testURL string,
	repeat int32,
	timeoutSeconds int32,
) (string, error) {
	outboundTag = strings.TrimSpace(outboundTag)
	if outboundTag == "" {
		return "", E.New("outbound tag is empty")
	}
	testURL = strings.TrimSpace(testURL)
	if testURL == "" {
		testURL = cfDataDefaultTraceURL
	}
	if repeat <= 0 {
		repeat = cfDataDefaultLatencyRepeat
	}
	if repeat > cfDataMaxLatencyRepeat {
		repeat = cfDataMaxLatencyRepeat
	}
	if timeoutSeconds <= 0 {
		timeoutSeconds = int32(cfDataDefaultLatencyTimeout / time.Second)
	}
	if time.Duration(timeoutSeconds)*time.Second > cfDataMaxLatencyTimeout {
		timeoutSeconds = int32(cfDataMaxLatencyTimeout / time.Second)
	}

	ctx, cancel := context.WithTimeout(s.ctx, time.Duration(repeat)*time.Duration(timeoutSeconds)*time.Second+10*time.Second)
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
	transport := newCFDataOutboundTransport(resolvedDialer, instance.ctx, time.Duration(timeoutSeconds)*time.Second)
	client := &http.Client{Transport: transport}
	defer transport.CloseIdleConnections()

	result := cfDataTrueLatencyResult{
		TotalAttempts: int(repeat),
		Results:       make([]cfDataTraceAttempt, 0, repeat),
		Mode:          "android-sfa-style-libbox-true-http",
	}
	var totalLatency int64
	for attempt := 1; attempt <= int(repeat); attempt++ {
		probe := runCFDataTraceAttempt(ctx, client, testURL, time.Duration(timeoutSeconds)*time.Second, attempt)
		result.Results = append(result.Results, probe)
		if !probe.Success {
			continue
		}
		result.SuccessCount++
		totalLatency += probe.LatencyMs
		if result.MinLatencyMs == 0 || probe.LatencyMs < result.MinLatencyMs {
			result.MinLatencyMs = probe.LatencyMs
		}
		if probe.LatencyMs > result.MaxLatencyMs {
			result.MaxLatencyMs = probe.LatencyMs
		}
		if result.OutboundIP == "" {
			result.OutboundIP = probe.OutboundIP
		}
		if result.Colo == "" {
			result.Colo = probe.Colo
		}
	}
	result.LossRate = float64(int(repeat)-result.SuccessCount) * 100 / float64(repeat)
	result.Success = result.SuccessCount > 0
	if result.SuccessCount > 0 {
		result.AvgLatencyMs = float64(totalLatency) / float64(result.SuccessCount)
	}
	if !result.Success {
		result.Error = firstCFDataError(result.Results)
		if result.Error == "" {
			result.Error = "all true HTTP attempts failed"
		}
	}

	raw, err := json.Marshal(result)
	if err != nil {
		return "", E.Cause(err, "marshal CFData true latency result")
	}
	return string(raw), nil
}

// CFDataTrueSpeedTest measures continuous download throughput for a fixed
// window, matching CFData's original 6-second windowed speed methodology while
// routing every request through the selected sing-box outbound.
func (s *StartedService) CFDataTrueSpeedTest(
	outboundTag string,
	downloadURL string,
	durationSeconds int32,
) (string, error) {
	outboundTag = strings.TrimSpace(outboundTag)
	if outboundTag == "" {
		return "", E.New("outbound tag is empty")
	}
	downloadURL = strings.TrimSpace(downloadURL)
	if downloadURL == "" {
		return "", E.New("download URL is empty")
	}
	if durationSeconds <= 0 {
		durationSeconds = int32(cfDataDefaultSpeedDuration / time.Second)
	}
	if time.Duration(durationSeconds)*time.Second > cfDataMaxSpeedDuration {
		durationSeconds = int32(cfDataMaxSpeedDuration / time.Second)
	}

	ctx, cancel := context.WithTimeout(s.ctx, time.Duration(durationSeconds)*time.Second+20*time.Second)
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
	transport := newCFDataOutboundTransport(resolvedDialer, instance.ctx, time.Duration(durationSeconds)*time.Second)
	client := &http.Client{Transport: transport}
	defer transport.CloseIdleConnections()

	result := cfDataTrueSpeedResult{Mode: "android-sfa-style-libbox-true-download"}
	requestCtx, requestCancel := context.WithTimeout(ctx, time.Duration(durationSeconds)*time.Second+10*time.Second)
	defer requestCancel()
	req, err := http.NewRequestWithContext(requestCtx, http.MethodGet, downloadURL, nil)
	if err != nil {
		result.Error = err.Error()
		return marshalCFDataSpeedResult(result)
	}
	req.Close = true
	req.Header.Set("Accept-Encoding", "identity")
	req.Header.Set("Connection", "close")

	startRequest := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		result.Error = err.Error()
		result.DurationMs = time.Since(startRequest).Milliseconds()
		return marshalCFDataSpeedResult(result)
	}
	if resp.Body == nil {
		result.Error = "empty HTTP response body"
		return marshalCFDataSpeedResult(result)
	}
	defer resp.Body.Close()
	result.StatusCode = resp.StatusCode
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		result.Error = "unexpected HTTP status: " + resp.Status
		return marshalCFDataSpeedResult(result)
	}

	return marshalCFDataSpeedResult(runCFDataWindowedDownload(requestCtx, result, resp, durationSeconds))
}

func newCFDataOutboundTransport(resolvedDialer dialer.ResolveDialer, boxCtx context.Context, timeout time.Duration) *http.Transport {
	return &http.Transport{
		Proxy:                 nil,
		ForceAttemptHTTP2:     true,
		DisableKeepAlives:     true,
		TLSHandshakeTimeout:   timeout,
		ResponseHeaderTimeout: timeout,
		TLSClientConfig: &tls.Config{
			Time:       ntp.TimeFuncFromContext(boxCtx),
			RootCAs:    adapter.RootPoolFromContext(boxCtx),
			MinVersion: tls.VersionTLS12,
		},
		DialContext: func(ctx context.Context, networkName, address string) (net.Conn, error) {
			return resolvedDialer.DialContext(ctx, networkName, metadata.ParseSocksaddr(address))
		},
	}
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
	body, readErr := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	end := time.Now()
	if readErr != nil {
		result.Error = readErr.Error()
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
		result.TTFBMs = result.LatencyMs
	}
	if !connectStart.IsZero() && !connectDone.IsZero() && connectDone.After(connectStart) {
		result.OutboundDialMs = connectDone.Sub(connectStart).Milliseconds()
	}
	if !tlsStart.IsZero() && !tlsDone.IsZero() && tlsDone.After(tlsStart) {
		result.TLSHandshakeMs = tlsDone.Sub(tlsStart).Milliseconds()
	}
	traceValues := parseCFDataTrace(string(body))
	result.OutboundIP = traceValues["ip"]
	result.Colo = traceValues["colo"]
	result.Success = true
	return result
}

type cfDataReaderChunk struct {
	n   int
	err error
}

func runCFDataWindowedDownload(parent context.Context, result cfDataTrueSpeedResult, resp *http.Response, durationSeconds int32) cfDataTrueSpeedResult {
	duration := time.Duration(durationSeconds) * time.Second
	measureCtx, cancelMeasure := context.WithCancel(parent)
	defer cancelMeasure()

	chunks := make(chan cfDataReaderChunk, 16)
	readerDone := make(chan struct{})
	buffer := make([]byte, 128*1024)
	var once sync.Once

	go func() {
		defer close(readerDone)
		for {
			n, err := resp.Body.Read(buffer)
			select {
			case chunks <- cfDataReaderChunk{n: n, err: err}:
			case <-measureCtx.Done():
				return
			}
			if err != nil {
				return
			}
		}
	}()

	var total int64
	started := time.Now()
	deadline := time.NewTimer(duration)
	defer deadline.Stop()
	stopReader := func() {
		once.Do(func() {
			cancelMeasure()
			_ = resp.Body.Close()
		})
	}

	done := false
	for !done {
		select {
		case <-parent.Done():
			done = true
		case <-deadline.C:
			done = true
		case chunk := <-chunks:
			if chunk.n > 0 {
				total += int64(chunk.n)
			}
			if chunk.err != nil {
				done = true
			}
		}
	}
	elapsed := time.Since(started)
	stopReader()
	<-readerDone

	result.Bytes = total
	result.DurationMs = elapsed.Milliseconds()
	if total <= 0 {
		result.Error = "0 bytes received during speed window"
		return result
	}
	if elapsed <= 0 {
		result.Error = "speed measurement duration is zero"
		return result
	}
	result.SpeedMBps = float64(total) / elapsed.Seconds() / 1024 / 1024
	result.Speed = formatSpeed(result.SpeedMBps)
	result.Success = true
	return result
}

func firstCFDataError(results []cfDataTraceAttempt) string {
	for _, result := range results {
		if strings.TrimSpace(result.Error) != "" {
			return result.Error
		}
	}
	return ""
}

func parseCFDataTrace(text string) map[string]string {
	values := make(map[string]string)
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.TrimSpace(parts[0])
		value := strings.TrimSpace(parts[1])
		if key != "" && value != "" {
			values[key] = value
		}
	}
	return values
}

func formatSpeed(mibPerSecond float64) string {
	return strconv.FormatFloat(mibPerSecond, 'f', 2, 64) + " MB/s"
}

func marshalCFDataSpeedResult(result cfDataTrueSpeedResult) (string, error) {
	raw, err := json.Marshal(result)
	if err != nil {
		return "", E.Cause(err, "marshal CFData true speed result")
	}
	return string(raw), nil
}
