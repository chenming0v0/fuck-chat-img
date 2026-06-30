package proxy

import (
	"bytes"
	"encoding/json"
)

// normField 规范化过程中的字段(用于确定性排序)
type normField struct {
	key string
	val json.RawMessage
}

// normalizeResponsesInput 将 Responses API 的 input 数组规范化为稳定的 canonical JSON。
//
// 关键点:
//   - 对每个 JSON 对象的 key 做确定性排序(保证字段顺序不影响缓存键)
//   - 把图片内容(input_image)替换为稳定的 content hash 占位,
//     使"相同图片"无论以 url 还是 base64 传入都产生相同缓存键
//   - 剔除易变字段(如临时 id、时间戳), 仅保留语义字段
//
// 注意: 消息数组和 content 数组的顺序是语义的一部分, 绝不能排序!
// 返回的 canonicalInput 仅用于计算缓存键, 不改变原始请求.
func normalizeResponsesInput(input json.RawMessage) []byte {
	// input 可能是数组或单条对象
	var arr []json.RawMessage
	if err := json.Unmarshal(input, &arr); err != nil {
		// input 也可能是单条 message 对象
		var obj json.RawMessage
		if err := json.Unmarshal(input, &obj); err != nil {
			return canonicalizeRaw(input)
		}
		arr = []json.RawMessage{obj}
	}

	out := bytes.NewBuffer(nil)
	out.WriteByte('[')
	for i, item := range arr {
		if i > 0 {
			out.WriteByte(',')
		}
		out.Write(normalizeMessageItem(item))
	}
	out.WriteByte(']')
	return out.Bytes()
}

func normalizeMessageItem(item json.RawMessage) []byte {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(item, &m); err != nil {
		return canonicalizeRaw(item)
	}
	var fields []normField
	for k, v := range m {
		// 剔除易变字段
		if k == "id" || k == "created_at" || k == "timestamp" {
			continue
		}
		if k == "content" {
			fields = append(fields, normField{key: k, val: normalizeContentArray(v)})
			continue
		}
		fields = append(fields, normField{key: k, val: canonicalizeRaw(v)})
	}
	// 对对象的 key 排序(不影响语义, 仅保证确定性序列化)
	sortNormFields(fields)
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

func sortNormFields(fields []normField) {
	// 简单插入排序(字段数通常很少)
	for i := 1; i < len(fields); i++ {
		for j := i; j > 0 && fields[j-1].key > fields[j].key; j-- {
			fields[j-1], fields[j] = fields[j], fields[j-1]
		}
	}
}

// normalizeContentArray 规范化 content 数组, 并把图片替换为稳定占位
// 注意: content 数组顺序保持不变(顺序影响语义)
func normalizeContentArray(raw json.RawMessage) []byte {
	var arr []map[string]interface{}
	if err := json.Unmarshal(raw, &arr); err != nil {
		return canonicalizeRaw(raw)
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
		var fields []normField
		for k, v := range c {
			if k == "id" {
				continue
			}
			vb, _ := json.Marshal(v)
			fields = append(fields, normField{key: k, val: vb})
		}
		sortNormFields(fields)
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
	// 注意: 不对 content 数组排序, 保持原始顺序
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

// canonicalizeRaw 递归对任意 JSON 做确定性序列化(排序 map key, 保持数组顺序)
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
		sortStrings(keys)
		return orderedMap(t, keys)
	case []interface{}:
		// 数组顺序保持不变
		out := make([]interface{}, len(t))
		for i, e := range t {
			out[i] = canonicalizeValue(e)
		}
		return out
	default:
		return v
	}
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j-1] > s[j]; j-- {
			s[j-1], s[j] = s[j], s[j-1]
		}
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
		out = append(out, kvPair{k: k, v: canonicalizeValue(m[k])})
	}
	return out
}

