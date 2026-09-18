package libbox

// CFDataTrueTest exposes a small CFData-specific test entry point while
// keeping the actual connection logic inside sing-box's daemon layer. The
// caller must have an already started CommandServer instance; the method does
// not spawn a process, require root, or use an inbound proxy.
func (s *CommandServer) CFDataTrueTest(
	outboundTag string,
	testURL string,
	downloadURL string,
	repeat int32,
	timeoutSeconds int32,
	downloadBytes int64,
) (string, error) {
	return s.StartedService.CFDataTrueTest(
		outboundTag,
		testURL,
		downloadURL,
		repeat,
		timeoutSeconds,
		downloadBytes,
	)
}
