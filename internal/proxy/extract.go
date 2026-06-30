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
// 就调用图片模型识别, 并根据 ReplaceImage 配置决定:
//   - true : 用文本描述替换图片项
//   - false: 保留原图, 并在其后追加一条文本描述项(仅当父节点为数组时可追加)
//
// 不同协议的文本项 type 不同:
//   - Chat / Messages : "text"
//   - Responses       : "input_text"
// 通过 imgProcessConfig.textType 由调用方传入, 保证递归处理器产出的类型符合各协议规范.

// imgProcessConfig 递归图片处理的配置
type imgProcessConfig struct {
	replaceImage bool   // true=替换图片; false=保留原图并追加文本
	textType     string // 文本项的 type 字段值(chat/messages="text", responses="input_text")
}

// defaultImgConfig 默认配置(向后兼容: 替换 + text 类型)
func defaultImgConfig(g *modelGroupRuntime) imgProcessConfig {
	return imgProcessConfig{
		replaceImage: g.ReplaceImage,
		textType:     "text",
	}
}

// imgResult 递归处理过程中的累计统计
type imgResult struct {
	hasImage bool
	imgCount int
	imgModel string // 使用的图片模型(取最后一次识别所用模型)
	modified bool   // 是否产生修改(用于判断是否需要重新序列化)
	err      error  // 第一个发生的错误
}

// processImagesInValue 递归处理任意 JSON 值, 识别其中所有图片并按配置替换/追加文本.
//
// 入参 v 是已解析的任意 JSON 值(map/slice/string/number/bool/nil).
// 返回处理后的值与统计. 出错时返回的 v 为原值, err 含错误.
//
// 规则:
//  1. slice: 逐元素处理; 若元素是图片项, 按 cfg.replaceImage 决定替换或追加
//  2. map 是"图片 content item"(任意已知格式) -> 识别并替换为 {type: cfg.textType, text: ...}
//     (此分支仅在 v 本身就是单个图片项时触发; 数组内的图片项由 slice 分支处理以支持追加)
//  3. map 不是图片 -> 递归处理每个 value, 原地写回
//  4. string -> 尝试解析为 JSON 后递归; 或检测 data: URL 直接识别
//  5. 其它原样返回
func processImagesInValue(g *modelGroupRuntime, v interface{}) (interface{}, imgResult) {
	return processImagesInValueCfg(g, v, defaultImgConfig(g))
}

// processImagesInValueCfg 带配置的递归处理
func processImagesInValueCfg(g *modelGroupRuntime, v interface{}, cfg imgProcessConfig) (interface{}, imgResult) {
	res := imgResult{}
	switch t := v.(type) {
	case []interface{}:
		// 1. 数组: 构建新切片, 支持"保留原图 + 追加文本"
		out := make([]interface{}, 0, len(t))
		for _, elem := range t {
			// 若元素是图片项, 在数组层处理(才能支持追加)
			if cm, ok := elem.(map[string]interface{}); ok && isImageContentItem(cm) {
				url, b64, ok := extractImageRef(cm)
				if ok {
					imgs := nextImageModels(g)
					desc, used, err := recognizeImage(imgs, g.ImageStrategy, g.ImagePrompt, url, b64, sharedHTTPClient)
					if err != nil {
						res.hasImage = true
						res.imgCount++
						if used != "" {
							res.imgModel = used
						}
						res.err = err
						// 出错仍保留原元素
						out = append(out, elem)
						return out, res
					}
					res.hasImage = true
					res.imgCount++
					if used != "" {
						res.imgModel = used
					}
					res.modified = true
					textItem := map[string]interface{}{
						"type": cfg.textType,
						"text": "[图片识别结果]\n" + desc,
					}
					if cfg.replaceImage {
						out = append(out, textItem)
					} else {
						// 保留原图, 追加文本描述
						out = append(out, elem, textItem)
					}
					continue
				}
			}
			// 非图片项: 递归处理
			newVal, sub := processImagesInValueCfg(g, elem, cfg)
			mergeResult(&res, &sub)
			out = append(out, newVal)
			if sub.err != nil {
				return out, res
			}
		}
		return out, res

	case map[string]interface{}:
		// 2. v 本身是单个图片项(罕见, 通常发生在直接以单个 item 调用时): 替换为文本
		//    (无法追加, 因为没有父数组; 此处采用替换语义)
		if isImageContentItem(t) {
			url, b64, ok := extractImageRef(t)
			if ok {
				imgs := nextImageModels(g)
				desc, used, err := recognizeImage(imgs, g.ImageStrategy, g.ImagePrompt, url, b64, sharedHTTPClient)
				if err != nil {
					res.hasImage = true
					res.imgCount = 1
					if used != "" {
						res.imgModel = used
					}
					res.err = err
					return t, res
				}
				res.hasImage = true
				res.imgCount = 1
				if used != "" {
					res.imgModel = used
				}
				res.modified = true
				return map[string]interface{}{
					"type": cfg.textType,
					"text": "[图片识别结果]\n" + desc,
				}, res
			}
		}
		// 3. 不是图片 item: 递归处理每个字段值, 原地写回
		for k, val := range t {
			newVal, sub := processImagesInValueCfg(g, val, cfg)
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

	case string:
		// 4. 字符串: 优先尝试 JSON 解析后递归
		s := strings.TrimSpace(t)
		if (strings.HasPrefix(s, "{") && strings.HasSuffix(s, "}")) ||
			(strings.HasPrefix(s, "[") && strings.HasSuffix(s, "]")) {
			var inner interface{}
			if err := json.Unmarshal([]byte(s), &inner); err == nil {
				newInner, sub := processImagesInValueCfg(g, inner, cfg)
				mergeResult(&res, &sub)
				if sub.hasImage {
					if newBytes, mErr := json.Marshal(newInner); mErr == nil {
						return string(newBytes), res
					}
				}
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
				if used != "" {
					res.imgModel = used
				}
				res.err = err
				return t, res
			}
			res.hasImage = true
			res.imgCount = 1
			if used != "" {
				res.imgModel = used
			}
			res.modified = true
			return "[图片识别结果]\n" + desc, res
		}
		return t, res

	default:
		return v, res
	}
}

// mergeResult 把 sub 的统计合并进 res(含 modified 标志, 保证递归层层向上传播)
func mergeResult(res, sub *imgResult) {
	if sub.hasImage {
		res.hasImage = true
		res.imgCount += sub.imgCount
		if sub.imgModel != "" {
			res.imgModel = sub.imgModel
		}
	}
	if sub.modified {
		res.modified = true
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
	return processImagesInStringContentCfg(g, s, defaultImgConfig(g))
}

// processImagesInStringContentCfg 带配置的字符串内容处理
func processImagesInStringContentCfg(g *modelGroupRuntime, s string, cfg imgProcessConfig) (string, bool, int, string, error) {
	stripped := strings.TrimSpace(s)
	if stripped == "" {
		return s, false, 0, "", nil
	}
	// 1. 尝试作为 JSON 解析
	if (strings.HasPrefix(stripped, "{") && strings.HasSuffix(stripped, "}")) ||
		(strings.HasPrefix(stripped, "[") && strings.HasSuffix(stripped, "]")) {
		var v interface{}
		if err := json.Unmarshal([]byte(stripped), &v); err == nil {
			newV, r := processImagesInValueCfg(g, v, cfg)
			if r.err != nil {
				return s, r.hasImage, r.imgCount, r.imgModel, r.err
			}
			if !r.hasImage {
				return s, false, 0, "", nil
			}
			newBytes, mErr := json.Marshal(newV)
			if mErr != nil {
				return s, r.hasImage, r.imgCount, r.imgModel, mErr
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
func processMessageContent(g *modelGroupRuntime, content interface{}) (interface{}, bool, int, string, error) {
	return processMessageContentCfg(g, content, defaultImgConfig(g))
}

// processMessageContentCfg 带配置的单条 message content 处理
func processMessageContentCfg(g *modelGroupRuntime, content interface{}, cfg imgProcessConfig) (interface{}, bool, int, string, error) {
	switch t := content.(type) {
	case []interface{}:
		newV, r := processImagesInValueCfg(g, t, cfg)
		return newV, r.hasImage, r.imgCount, r.imgModel, r.err
	case string:
		return processImagesInStringContentCfg(g, t, cfg)
	default:
		return content, false, 0, "", nil
	}
}
