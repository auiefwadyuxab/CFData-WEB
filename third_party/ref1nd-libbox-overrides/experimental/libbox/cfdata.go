package libbox

// CFDataTrueLatencyTest exposes CFData's real outbound HTTP latency test to
// Android through gomobile. The actual transport stays inside daemon so it can
// access the running sing-box instance and outbound manager directly.
func (s *CommandServer) CFDataTrueLatencyTest(
	outboundTag string,
	testURL string,
	repeat int32,
	timeoutSeconds int32,
) (string, error) {
	return s.StartedService.CFDataTrueLatencyTest(
		outboundTag,
		testURL,
		repeat,
		timeoutSeconds,
	)
}

// CFDataTrueSpeedTest exposes CFData's fixed-window real download test to
// Android through gomobile.
func (s *CommandServer) CFDataTrueSpeedTest(
	outboundTag string,
	downloadURL string,
	durationSeconds int32,
) (string, error) {
	return s.StartedService.CFDataTrueSpeedTest(
		outboundTag,
		downloadURL,
		durationSeconds,
	)
}
