package proxy

import "testing"

func TestExtractUsageMessagesAnthropicNoDoubleCount(t *testing.T) {
	body := []byte(`{"usage":{"input_tokens":100,"output_tokens":50,"cache_creation_input_tokens":20,"cache_read_input_tokens":30}}`)
	pt, ct := extractUsageMessages(body)
	if pt != 100 {
		t.Errorf("Anthropic input_tokens 已包含缓存 tokens, pt 应为 100, 实际 %d (旧bug会得到150)", pt)
	}
	if ct != 50 {
		t.Errorf("ct 应为 50, 实际 %d", ct)
	}
}

func TestExtractUsageMessagesCacheOnly(t *testing.T) {
	body := []byte(`{"usage":{"cache_creation_input_tokens":30,"cache_read_input_tokens":20,"output_tokens":10}}`)
	pt, ct := extractUsageMessages(body)
	if pt != 50 {
		t.Errorf("无 input_tokens 时应以缓存字段之和为准, pt 应为 50, 实际 %d", pt)
	}
	if ct != 10 {
		t.Errorf("ct 应为 10, 实际 %d", ct)
	}
}

func TestExtractUsageOpenAI(t *testing.T) {
	body := []byte(`{"usage":{"prompt_tokens":200,"completion_tokens":80}}`)
	pt, ct := extractUsage(body)
	if pt != 200 || ct != 80 {
		t.Errorf("OpenAI 风格: pt=%d ct=%d, 期望 200/80", pt, ct)
	}
}

func TestExtractUsageNoUsage(t *testing.T) {
	body := []byte(`{"id":"x","object":"chat.completion"}`)
	pt, ct := extractUsage(body)
	if pt != 0 || ct != 0 {
		t.Errorf("无 usage 字段时应返回 0,0, 实际 %d,%d", pt, ct)
	}
}

func TestUpdateUsageFromSSEAnthropic(t *testing.T) {
	line := []byte("data: {\"type\":\"message_delta\",\"usage\":{\"input_tokens\":100,\"output_tokens\":50,\"cache_creation_input_tokens\":20}}")
	pt, ct := updateUsageFromSSE(line, 0, 0)
	if pt != 100 {
		t.Errorf("SSE 中 Anthropic usage input_tokens 已含缓存, pt 应为 100, 实际 %d", pt)
	}
	if ct != 50 {
		t.Errorf("ct 应为 50, 实际 %d", ct)
	}
}
