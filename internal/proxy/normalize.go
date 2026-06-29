package proxy

import (
	"bytes"
	"encoding/json"
	"sort"
)

// normalizeResponsesInput 将 Responses API 的 input 数组规范化为稳定的 canonical JSON。
//
// 关键点(满足"每一次都把数组传上来的东西替换成正确的"缓存要求):
//   - 对 input 数组进行确定性排序(按 role + 内容哈希), 保证相同语义不同顺序也能命中
//   - 对每个 content 数组同样排序
//   - 把图片内容(input_image)替换为稳定的 content hash 占位,
//     使"相同图片"无论以 url 还是 base64 传入都产生相同缓存键
//   - 剔除易变字段(如临时 id、时间戳), 仅保留语义字段
//
// 返回的 canonicalInput 仅用于计算缓存键, 不改变原始请求.
func normalizeResponsesInput(input json.RawMessage) []byte {
	var arr []map[string]json.RawMessage
	if err := json.Unmarshal(input, &arr); err != nil {
		// input 也可能是单条 message 对象
		var obj map[string]json.RawMessage
		if err := json.Unmarshal(input, &obj); err != nil {
			return canonicalizeRaw(input)
		}
		arr = []map[string]json.RawMessage{obj}
	}

	keys := make([]string, 0, len(arr))
	encoded := make(map[string][]byte, len(arr))
	for _, item := range arr {
		b := normalizeMessageItem(item)
		keys = append(keys, string(b))
		encoded[string(b)] = b
	}
	sort.Strings(keys)

	out := bytes.NewBuffer(nil)
	out.WriteByte('[')
	for i, k := range keys {
		if i > 0 {
			out.WriteByte(',')
		}
		out.Write(encoded[k])
	}
	out.WriteByte(']')
	return out.Bytes()
}

func normalizeMessageItem(item map[string]json.RawMessage) []byte {
	type field struct {
		key string
		val json.RawMessage
	}
	var fields []field
	for k, v := range item {
		// 剔除易变字段
		if k == "id" || k == "created_at" || k == "timestamp" {
			continue
		}
		if k == "content" {
			fields = append(fields, field{key: k, val: normalizeContentArray(v)})
			continue
		}
		fields = append(fields, field{key: k, val: canonicalizeRaw(v)})
	}
	sort.Slice(fields, func(i, j int) bool { return fields[i].key < fields[j].key })
	out := bytes.NewBuffer(nil)
	out.WriteByte('{')
	for i, f := range fields {
		if i > 0 {
			out.WriteByte(',')
		}
		kb, _ := json.Marshal(f.key)
		out.Write(kb)
		out.WriteByte(':')
		out.Write(f.val)
	}
	out.WriteByte('}')
	return out.Bytes()
}

// normalizeContentArray 规范化 content 数组, 并把图片替换为稳定占位
func normalizeContentArray(raw json.RawMessage) []byte {
	var arr []map[string]interface{}
	if err := json.Unmarshal(raw, &arr); err != nil {
		return canonicalizeRaw(raw)
	}
	type field struct {
		key string
		val json.RawMessage
	}
	encoded := make([][]byte, 0, len(arr))
	for _, c := range arr {
		typ, _ := c["type"].(string)
		if typ == "input_image" || typ == "image" || typ == "image_url" {
			// 用图片内容哈希替换, 保证相同图片命中
			h := imageContentHash(c)
			b, _ := json.Marshal(map[string]string{"type": typ, "img": h})
			encoded = append(encoded, b)
			continue
		}
		// 文本类型: 保留文本, 排序其它字段
		var fields []field
		for k, v := range c {
			if k == "id" {
				continue
			}
			vb, _ := json.Marshal(v)
			fields = append(fields, field{key: k, val: vb})
		}
		sort.Slice(fields, func(i, j int) bool { return fields[i].key < fields[j].key })
		out := bytes.NewBuffer(nil)
		out.WriteByte('{')
		for i, f := range fields {
			if i > 0 {
				out.WriteByte(',')
			}
			kb, _ := json.Marshal(f.key)
			out.Write(kb)
			out.WriteByte(':')
			out.Write(f.val)
		}
		out.WriteByte('}')
		encoded = append(encoded, out.Bytes())
	}
	// 对 content 数组按字节排序
	sort.Slice(encoded, func(i, j int) bool { return bytes.Compare(encoded[i], encoded[j]) < 0 })
	out := bytes.NewBuffer(nil)
	out.WriteByte('[')
	for i, b := range encoded {
		if i > 0 {
			out.WriteByte(',')
		}
		out.Write(b)
	}
	out.WriteByte(']')
	return out.Bytes()
}

// canonicalizeRaw 递归对任意 JSON 做确定性序列化(排序 map key)
func canonicalizeRaw(raw json.RawMessage) []byte {
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

func canonicalizeValue(v interface{}) interface{} {
	switch t := v.(type) {
	case map[string]interface{}:
		keys := make([]string, 0, len(t))
		for k := range t {
			if k == "id" || k == "created_at" || k == "timestamp" {
				continue
			}
			keys = append(keys, k)
		}
		sort.Strings(keys)
		out := make(map[string]interface{}, len(keys))
		for _, k := range keys {
			out[k] = canonicalizeValue(t[k])
		}
		// 用有序序列化
		return orderedMap(out, keys)
	case []interface{}:
		out := make([]interface{}, len(t))
		for i, e := range t {
			out[i] = canonicalizeValue(e)
		}
		return out
	default:
		return v
	}
}

// orderedMap 用切片保证 map 序列化顺序
type kvPair struct {
	k string
	v interface{}
}

type orderedKVs []kvPair

func (o orderedKVs) MarshalJSON() ([]byte, error) {
	buf := bytes.NewBuffer(nil)
	buf.WriteByte('{')
	for i, p := range o {
		if i > 0 {
			buf.WriteByte(',')
		}
		kb, _ := json.Marshal(p.k)
		buf.Write(kb)
		buf.WriteByte(':')
		vb, err := json.Marshal(p.v)
		if err != nil {
			return nil, err
		}
		buf.Write(vb)
	}
	buf.WriteByte('}')
	return buf.Bytes(), nil
}

func orderedMap(m map[string]interface{}, keys []string) interface{} {
	out := make(orderedKVs, 0, len(keys))
	for _, k := range keys {
		out = append(out, kvPair{k: k, v: m[k]})
	}
	return out
}
