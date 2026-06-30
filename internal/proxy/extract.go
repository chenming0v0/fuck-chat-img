package proxy

import (
	"encoding/json"
	"strings"
)

// 本文件处理"任意嵌套深度"的图片识别需求.
//
// 背景:
//   - 直接 content 数组项: {type: image_url/input_image/image, ...} 已在 chat.go/responses.go 中处理
//   - 但 Codex 等 agent 工具(如 view_image)会把截图以 base64 嵌入:
//       1) role=tool 的消息, content 是 JSON 字符串, 内含 base64 图片
//       2) role=user 的重试/降级消息, content 是数组, item.type=tool_result,
//          item.content 是 JSON 字符串, 字符串内含 base64 图片
//   - Claude /v1/messages 还会出现 {type: image, source: {type: base64, data}} 格式
//
// 因此需要一个递归处理器: 任意 JSON 节点(对象/数组/字符串)中只要出现可识别图片,
// 就调用图片模型识别, 并把图片替换为文本描述.

// imgResult 递归处理过程中的累计统计
type imgResult struct {
	hasImage bool
	imgCount int
	imgModel string // 使用的图片模型(取最后一次识别所用模型)
	modified bool   // 是否产生修改(用于判断是否需要重新序列化)
	err      error  // 第一个发生的错误
}

// processImagesInValue 递归处理任意 JSON 值, 识别其中所有图片并替换为文本.
//
// 入参 v 是已解析的任意 JSON 值(map/slice/string/number/bool/nil).
// 返回处理后的值与统计. 出错时返回的 v 为原值, err 含错误.
//
// 规则:
//  1. map 是"图片 content item"(任意已知格式) -> 识别并整体替换为 {type: text, text: ...}
//  2. map 不是图片 -> 递归处理每个 value, 原地写回
//  3. slice -> 递归处理每个元素, 原地写回
//  4. string -> 尝试解析为 JSON 后递归; 或检测 data: URL 直接识别
//  5. 其它原样返回
func processImagesInValue(g *modelGroupRuntime, v interface{}) (interface{}, imgResult) {
	res := imgResult{}
	switch t := v.(type) {
	case map[string]interface{}:
		// 1. 先判断是不是图片 item
		if isImageContentItem(t) {
			url, b64, ok := extractImageRef(t)
			if ok {
				imgs := nextImageModels(g)
				desc, used, err := recognizeImage(imgs, g.ImageStrategy, g.ImagePrompt, url, b64, sharedHTTPClient)
				if err != nil {
					res.hasImage = true
					res.imgCount = 1
					res.imgModel = used
					res.err = err
					return t, res
				}
				res.hasImage = true
				res.imgCount = 1
				res.imgModel = used
				res.modified = true
				// 用文本替换整个图片 item
				return map[string]interface{}{
					"type": "text",
					"text": "[图片识别结果]\n" + desc,
				}, res
			}
		}
		// 2. 不是图片 item: 递归处理每个字段值, 原地写回
		for k, val := range t {
			newVal, sub := processImagesInValue(g, val)
			mergeResult(&res, &sub)
			if sub.modified {
				t[k] = newVal
				res.modified = true
			}
			if sub.err != nil {
				return t, res
			}
		}
		return t, res

	case []interface{}:
		// 3. 递归处理数组元素, 顺序不变, 原地写回
		for i, elem := range t {
			newVal, sub := processImagesInValue(g, elem)
			mergeResult(&res, &sub)
			if sub.modified {
				t[i] = newVal
				res.modified = true
			}
			if sub.err != nil {
				return t, res
			}
		}
		return t, res

	case string:
		// 4. 字符串: 优先尝试 JSON 解析后递归
		s := strings.TrimSpace(t)
		if (strings.HasPrefix(s, "{") && strings.HasSuffix(s, "}")) ||
			(strings.HasPrefix(s, "[") && strings.HasSuffix(s, "]")) {
			var inner interface{}
			if err := json.Unmarshal([]byte(s), &inner); err == nil {
				newInner, sub := processImagesInValue(g, inner)
				if sub.hasImage {
					if newBytes, mErr := json.Marshal(newInner); mErr == nil {
						res.hasImage = true
						res.imgCount = sub.imgCount
						res.imgModel = sub.imgModel
						res.err = sub.err
						res.modified = true
						return string(newBytes), res
					}
				}
				// 解析为 JSON 但没找到图片或序列化失败: 返回原值
				return t, res
			}
		}
		// 5. 检测 data URL(单独的 base64 图片字符串)
		if isDataURLString(t) {
			imgs := nextImageModels(g)
			desc, used, err := recognizeImage(imgs, g.ImageStrategy, g.ImagePrompt, "", t, sharedHTTPClient)
			if err != nil {
				res.hasImage = true
				res.imgCount = 1
				res.imgModel = used
				res.err = err
				return t, res
			}
			res.hasImage = true
			res.imgCount = 1
			res.imgModel = used
			res.modified = true
			return "[图片识别结果]\n" + desc, res
		}
		return t, res

	default:
		return v, res
	}
}

// mergeResult 把 sub 的统计合并进 res(不含 modified)
func mergeResult(res, sub *imgResult) {
	if sub.hasImage {
		res.hasImage = true
		res.imgCount += sub.imgCount
		if sub.imgModel != "" {
			res.imgModel = sub.imgModel
		}
	}
	if sub.err != nil && res.err == nil {
		res.err = sub.err
	}
}

// isDataURLString 判断字符串是否为 data:image/...;base64,... 形式
func isDataURLString(s string) bool {
	if !strings.HasPrefix(s, "data:") {
		return false
	}
	// 必须包含 ;base64, 后跟实际数据
	idx := strings.Index(s, ";base64,")
	if idx < 0 {
		return false
	}
	// 防止误判过短字符串
	return len(s) > len("data:;base64,")+16
}

// processImagesInStringContent 处理"字符串形式的 content"
// 适用场景:
//   - role=tool 的 message.content 是 JSON 字符串, 内含 base64 图片
//   - tool_result item.content 是 JSON 字符串, 内含 base64 图片
//
// 返回: 修改后的字符串, 是否识别到图片, 图片数量, 使用的图片模型, error
func processImagesInStringContent(g *modelGroupRuntime, s string) (string, bool, int, string, error) {
	stripped := strings.TrimSpace(s)
	if stripped == "" {
		return s, false, 0, "", nil
	}
	// 1. 尝试作为 JSON 解析
	if (strings.HasPrefix(stripped, "{") && strings.HasSuffix(stripped, "}")) ||
		(strings.HasPrefix(stripped, "[") && strings.HasSuffix(stripped, "]")) {
		var v interface{}
		if err := json.Unmarshal([]byte(stripped), &v); err == nil {
			newV, r := processImagesInValue(g, v)
			if r.err != nil {
				return s, r.hasImage, r.imgCount, r.imgModel, r.err
			}
			if !r.hasImage {
				return s, false, 0, "", nil
			}
			newBytes, mErr := json.Marshal(newV)
			if mErr != nil {
				return s, r.hasImage, r.imgCount, r.imgModel, nil
			}
			return string(newBytes), true, r.imgCount, r.imgModel, nil
		}
	}
	// 2. 不是 JSON, 但可能是单独的 data URL
	if isDataURLString(stripped) {
		imgs := nextImageModels(g)
		desc, used, err := recognizeImage(imgs, g.ImageStrategy, g.ImagePrompt, "", stripped, sharedHTTPClient)
		if err != nil {
			return s, true, 1, used, err
		}
		return "[图片识别结果]\n" + desc, true, 1, used, nil
	}
	return s, false, 0, "", nil
}

// processMessageContent 处理单条 message 的 content 字段
// 兼容三种 content 形态:
//  1. 数组(标准 content array): 递归处理
//  2. 字符串(Codex tool 输出等): 走 processImagesInStringContent
//  3. 其它: 原样返回
//
// 注意: Codex 场景下保留原图会浪费 token 且无意义, 因此统一替换为文本描述,
// 不区分 ReplaceImage 配置.
func processMessageContent(g *modelGroupRuntime, content interface{}) (interface{}, bool, int, string, error) {
	switch t := content.(type) {
	case []interface{}:
		newV, r := processImagesInValue(g, t)
		return newV, r.hasImage, r.imgCount, r.imgModel, r.err
	case string:
		return processImagesInStringContent(g, t)
	default:
		return content, false, 0, "", nil
	}
}
