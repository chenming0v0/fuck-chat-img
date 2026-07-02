package proxy

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
)

// 本文件负责把任意输入规范化为"确定性 canonical JSON", 用于计算缓存键.
//
// 核心规则:
//  1. 对象 key 排序(Go 的 json.Marshal 自动对 map[string] 排序), 数组顺序保持不变
//     (LLM 上下文中消息/内容顺序影响语义, 绝不能排序数组)
//  2. 任意已知格式的图片(OpenAI image_url / Responses input_image / Claude image+source /
//     嵌入在 JSON 字符串中的 base64 data URL)都替换为 {"img": <content-hash>},
//     使"相同图片任意格式/任意嵌套深度"都产生相同缓存键
//  3. 易变字段(id / created_at / timestamp)被剔除
//  4. 字符串值若本身是合法 JSON, 会递归规范化后再序列化回去
//     (Codex 工具常把图片 JSON 序列化成字符串嵌在 content 中)
//
// 这样设计后, 一个统一的 canonicalizeValue 即可处理:
//   - OpenAI Chat 的 messages
//   - OpenAI Responses 的 input
//   - Anthropic Messages 的 messages
//   - 任意嵌套深度的 tool_result / role=tool / 字符串内 JSON
//
// 额外: 缓存键必须区分"影响输出的请求级参数"(stream / max_tokens / temperature /
// tools 等), 否则会出现"参数不同的两个请求命中同一缓存返回错误响应"的硬伤。
// paramsFingerprint 负责从原始请求体提取这些参数并产出确定性指纹。

// outputAffectingKeys 参与缓存键的"影响输出的请求级参数"白名单
// (messages/input/system 等内容字段不在此列, 它们走各自的 normalizeForCache)
//
// 该白名单必须覆盖所有会改变上游输出的请求级参数, 否则会出现"参数不同的两个请求
// 命中同一缓存返回错误响应"的硬伤(AGENTS.md 强调). 已纳入:
//   - 采样/解码参数: temperature, top_p, top_k, stop, seed, max_tokens, ...
//   - 工具相关: tools, tool_choice, parallel_tool_calls
//   - 输出形态: n, response_format, reasoning, reasoning_effort, thinking
//   - 偏置: frequency_penalty, presence_penalty, logit_bias, logprobs, top_logprobs
//   - 服务端状态依赖: previous_response_id, prompt_cache_key, store, include
//   - 系统指令: instructions(Responses), system 已由调用方规范化进 contentCanonical
var outputAffectingKeys = []string{
	"stream",
	"stream_options",
	"max_tokens",
	"max_output_tokens",
	"max_completion_tokens",
	"temperature",
	"top_p",
	"top_k",
	"stop",
	"stop_sequences",
	"tools",
	"tool_choice",
	"parallel_tool_calls",
	"reasoning",
	"reasoning_effort",
	"thinking",
	"response_format",
	"seed",
	"n",
	"frequency_penalty",
	"presence_penalty",
	"logit_bias",
	"logprobs",
	"top_logprobs",
	"modalities",
	"audio",
	"prediction",
	"instructions",
	"previous_response_id",
	"prompt_cache_key",
	"store",
	"include",
}

// paramsFingerprint 从原始请求体提取影响输出的参数, 返回确定性字节串
// (键按白名单顺序取, 值经 canonicalizeValue 规范化后 marshal, 保证确定性)
func paramsFingerprint(raw json.RawMessage) []byte {
	if len(raw) == 0 {
		return nil
	}
	var obj map[string]interface{}
	if err := json.Unmarshal(raw, &obj); err != nil {
		return nil
	}
	out := make(map[string]interface{}, len(outputAffectingKeys))
	for _, k := range outputAffectingKeys {
		if v, ok := obj[k]; ok {
			out[k] = canonicalizeValue(v)
		}
	}
	b, err := json.Marshal(out)
	if err != nil {
		return nil
	}
	return b
}

// normalizeForCache 把原始 JSON 规范化为确定性字节串(用于缓存键)
func normalizeForCache(raw json.RawMessage) []byte {
	if len(raw) == 0 {
		return nil
	}
	var v interface{}
	if err := json.Unmarshal(raw, &v); err != nil {
		return raw
	}
	b, err := json.Marshal(canonicalizeValue(v))
	if err != nil {
		return raw
	}
	return b
}

// composeCacheCanonical 把"输出影响参数指纹"与"内容规范化结果"拼接成最终 canonical
// 顺序固定为 params || NUL || content, 保证不同参数 / 不同内容都不会碰撞
func composeCacheCanonical(paramsFp []byte, content []byte) []byte {
	out := make([]byte, 0, len(paramsFp)+1+len(content))
	out = append(out, paramsFp...)
	out = append(out, 0)
	out = append(out, content...)
	return out
}

// canonicalizeValue 递归产生确定性表示
func canonicalizeValue(v interface{}) interface{} {
	switch t := v.(type) {
	case map[string]interface{}:
		// 1. 图片 content item: 整体替换为 {img: hash}
		if isImageContentItem(t) {
			if _, _, ok := extractImageRef(t); ok {
				h := imageContentHash(t)
				return map[string]interface{}{"img": h}
			}
		}
		// 2. 普通对象: 剔除易变字段后递归处理每个 value
		out := make(map[string]interface{}, len(t))
		for k, val := range t {
			if isVolatileKey(k) {
				continue
			}
			out[k] = canonicalizeValue(val)
		}
		return out

	case []interface{}:
		// 3. 数组: 保持顺序, 递归每个元素
		out := make([]interface{}, len(t))
		for i, e := range t {
			out[i] = canonicalizeValue(e)
		}
		return out

	case string:
		// 4. 字符串: 先尝试作为 JSON 递归规范化(Codex 工具常用形态)
		s := strings.TrimSpace(t)
		if (strings.HasPrefix(s, "{") && strings.HasSuffix(s, "}")) ||
			(strings.HasPrefix(s, "[") && strings.HasSuffix(s, "]")) {
			var inner interface{}
			if err := json.Unmarshal([]byte(s), &inner); err == nil {
				normalized := canonicalizeValue(inner)
				if b, mErr := json.Marshal(normalized); mErr == nil {
					return string(b)
				}
			}
		}
		// 5. 检测 data URL(单独的 base64 图片字符串)
		if isDataURLString(t) {
			h := hashDataURL(t)
			return map[string]interface{}{"img": h}
		}
		return t

	default:
		// number / bool / nil 原样返回
		return v
	}
}

// isVolatileKey 判断是否为易变字段(不应纳入缓存键)
func isVolatileKey(k string) bool {
	switch k {
	case "id", "created", "created_at", "timestamp", "updated_at":
		return true
	}
	return false
}

// hashDataURL 计算 data URL 字符串的稳定哈希(用于缓存键)
// 抽取 data:image/...;base64,XXX 中纯 base64 部分后哈希,
// 保证 media_type 不同但图片相同时也命中
func hashDataURL(s string) string {
	h := sha256.New()
	raw := stripDataURLPrefix(s)
	h.Write([]byte("b64:"))
	h.Write([]byte(raw))
	return hex.EncodeToString(h.Sum(nil))[:32]
}
