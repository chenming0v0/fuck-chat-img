package proxy

import (
	"context"
	"encoding/json"
	"strings"
)

type imgProcessConfig struct {
	replaceImage bool
	textType     string
}

func defaultImgConfig(g *modelGroupRuntime) imgProcessConfig {
	return imgProcessConfig{
		replaceImage: g.ReplaceImage,
		textType:     "text",
	}
}

type imgResult struct {
	hasImage bool
	imgCount int
	imgModel string
	modified bool
	err      error
}

func processImagesInValue(g *modelGroupRuntime, v interface{}, ctx context.Context) (interface{}, imgResult) {
	return processImagesInValueCfg(g, v, defaultImgConfig(g), ctx)
}

func processImagesInValueCfg(g *modelGroupRuntime, v interface{}, cfg imgProcessConfig, ctx context.Context) (interface{}, imgResult) {
	res := imgResult{}
	switch t := v.(type) {
	case []interface{}:
		out := make([]interface{}, 0, len(t))
		for _, elem := range t {
			if cm, ok := elem.(map[string]interface{}); ok && isImageContentItem(cm) {
				url, b64, ok := extractImageRef(cm)
				if ok {
					imgs := nextImageModels(g)
					desc, used, err := recognizeImage(ctx, imgs, g.ImageStrategy, g.ImagePrompt, url, b64, sharedHTTPClient)
					if err != nil {
						res.hasImage = true
						res.imgCount++
						if used != "" {
							res.imgModel = used
						}
						res.err = err
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
						out = append(out, elem, textItem)
					}
					continue
				}
			}
			newVal, sub := processImagesInValueCfg(g, elem, cfg, ctx)
			mergeResult(&res, &sub)
			out = append(out, newVal)
			if sub.err != nil {
				return out, res
			}
		}
		return out, res

	case map[string]interface{}:
		if isImageContentItem(t) {
			url, b64, ok := extractImageRef(t)
			if ok {
				imgs := nextImageModels(g)
				desc, used, err := recognizeImage(ctx, imgs, g.ImageStrategy, g.ImagePrompt, url, b64, sharedHTTPClient)
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
		for k, val := range t {
			newVal, sub := processImagesInValueCfg(g, val, cfg, ctx)
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
		s := strings.TrimSpace(t)
		if (strings.HasPrefix(s, "{") && strings.HasSuffix(s, "}")) ||
			(strings.HasPrefix(s, "[") && strings.HasSuffix(s, "]")) {
			var inner interface{}
			if err := json.Unmarshal([]byte(s), &inner); err == nil {
				newInner, sub := processImagesInValueCfg(g, inner, cfg, ctx)
				mergeResult(&res, &sub)
				if sub.hasImage {
					if newBytes, mErr := json.Marshal(newInner); mErr == nil {
						return string(newBytes), res
					}
				}
				return t, res
			}
		}
		if isDataURLString(t) {
			imgs := nextImageModels(g)
			desc, used, err := recognizeImage(ctx, imgs, g.ImageStrategy, g.ImagePrompt, "", t, sharedHTTPClient)
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

func isDataURLString(s string) bool {
	if !strings.HasPrefix(s, "data:") {
		return false
	}
	idx := strings.Index(s, ";base64,")
	if idx < 0 {
		return false
	}
	return len(s) > len("data:;base64,")+16
}

func processImagesInStringContent(g *modelGroupRuntime, s string, ctx context.Context) (string, bool, int, string, error) {
	return processImagesInStringContentCfg(g, s, defaultImgConfig(g), ctx)
}

func processImagesInStringContentCfg(g *modelGroupRuntime, s string, cfg imgProcessConfig, ctx context.Context) (string, bool, int, string, error) {
	stripped := strings.TrimSpace(s)
	if stripped == "" {
		return s, false, 0, "", nil
	}
	if (strings.HasPrefix(stripped, "{") && strings.HasSuffix(stripped, "}")) ||
		(strings.HasPrefix(stripped, "[") && strings.HasSuffix(stripped, "]")) {
		var v interface{}
		if err := json.Unmarshal([]byte(stripped), &v); err == nil {
			newV, r := processImagesInValueCfg(g, v, cfg, ctx)
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
	if isDataURLString(stripped) {
		imgs := nextImageModels(g)
		desc, used, err := recognizeImage(ctx, imgs, g.ImageStrategy, g.ImagePrompt, "", stripped, sharedHTTPClient)
		if err != nil {
			return s, true, 1, used, err
		}
		return "[图片识别结果]\n" + desc, true, 1, used, nil
	}
	return s, false, 0, "", nil
}

func processMessageContent(g *modelGroupRuntime, content interface{}, ctx context.Context) (interface{}, bool, int, string, error) {
	return processMessageContentCfg(g, content, defaultImgConfig(g), ctx)
}

func processMessageContentCfg(g *modelGroupRuntime, content interface{}, cfg imgProcessConfig, ctx context.Context) (interface{}, bool, int, string, error) {
	switch t := content.(type) {
	case []interface{}:
		newV, r := processImagesInValueCfg(g, t, cfg, ctx)
		return newV, r.hasImage, r.imgCount, r.imgModel, r.err
	case string:
		return processImagesInStringContentCfg(g, t, cfg, ctx)
	default:
		return content, false, 0, "", nil
	}
}
