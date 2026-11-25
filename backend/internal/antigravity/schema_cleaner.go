package antigravity

import (
	"fmt"
	"strings"
)

// CleanJSONSchema 清理并扁平化工具参数 JSON Schema，使其完全兼容 AntiGravity / Gemini v1internal 规范：
//   - 提取并展开 $defs / definitions 中的 $ref 引用（带循环引用防护）
//   - 合并 allOf 子模式（properties / required / type）
//   - 简化可空类型数组 ["string", "null"] 及 anyOf/oneOf 中的 null 分支
//   - 将 const 转为单元素 enum
//   - 移除上游不支持的关键字（$schema / additionalProperties / exclusiveMinimum / default 等）
//   - 保证 object 类型具备合法 properties 且 required 仅引用存在的字段
func CleanJSONSchema(schema map[string]any) map[string]any {
	if schema == nil {
		return nil
	}
	copied, ok := deepCopyJSONValue(schema).(map[string]any)
	if !ok || copied == nil {
		return nil
	}

	defs := extractSchemaDefs(copied)
	if len(defs) > 0 {
		flattenSchemaRefs(copied, defs, 0)
	}

	cleaned := cleanSchemaNode(copied)
	res, ok := cleaned.(map[string]any)
	if !ok || res == nil {
		return map[string]any{"type": "object", "properties": map[string]any{}}
	}
	if _, hasType := res["type"]; !hasType {
		res["type"] = "object"
	}
	if t, _ := res["type"].(string); strings.EqualFold(t, "object") {
		if _, hasProps := res["properties"]; !hasProps {
			res["properties"] = map[string]any{}
		}
	}
	return res
}

func extractSchemaDefs(schema map[string]any) map[string]any {
	defs := make(map[string]any)
	for _, key := range []string{"$defs", "definitions"} {
		if d, ok := schema[key].(map[string]any); ok {
			for k, v := range d {
				defs[k] = v
			}
			delete(schema, key)
		}
	}
	return defs
}

func flattenSchemaRefs(node map[string]any, defs map[string]any, depth int) {
	if depth > 12 || node == nil {
		return
	}
	if ref, ok := node["$ref"].(string); ok {
		delete(node, "$ref")
		parts := strings.Split(ref, "/")
		refName := parts[len(parts)-1]
		if target, exists := defs[refName]; exists {
			if targetMap, ok := target.(map[string]any); ok {
				for k, v := range targetMap {
					if _, already := node[k]; !already {
						node[k] = deepCopyJSONValue(v)
					}
				}
				flattenSchemaRefs(node, defs, depth+1)
			}
		}
	}
	for _, v := range node {
		switch sub := v.(type) {
		case map[string]any:
			flattenSchemaRefs(sub, defs, depth+1)
		case []any:
			for _, item := range sub {
				if itemMap, ok := item.(map[string]any); ok {
					flattenSchemaRefs(itemMap, defs, depth+1)
				}
			}
		}
	}
}

func deepCopyJSONValue(src any) any {
	switch v := src.(type) {
	case map[string]any:
		dst := make(map[string]any, len(v))
		for k, val := range v {
			dst[k] = deepCopyJSONValue(val)
		}
		return dst
	case []any:
		dst := make([]any, len(v))
		for i, val := range v {
			dst[i] = deepCopyJSONValue(val)
		}
		return dst
	default:
		return src
	}
}

var unsupportedSchemaKeys = map[string]struct{}{
	"$schema":               {},
	"$id":                   {},
	"$comment":              {},
	"$vocabulary":           {},
	"$defs":                 {},
	"definitions":           {},
	"$ref":                  {},
	"additionalProperties":  {},
	"patternProperties":     {},
	"propertyNames":         {},
	"exclusiveMinimum":      {},
	"exclusiveMaximum":      {},
	"minLength":             {},
	"maxLength":             {},
	"minimum":               {},
	"maximum":               {},
	"multipleOf":            {},
	"minItems":              {},
	"maxItems":              {},
	"uniqueItems":           {},
	"minProperties":         {},
	"maxProperties":         {},
	"pattern":               {},
	"default":               {},
	"examples":              {},
	"title":                 {},
	"readOnly":              {},
	"writeOnly":             {},
	"deprecated":            {},
	"contentEncoding":       {},
	"contentMediaType":      {},
	"unevaluatedProperties": {},
}

func cleanSchemaNode(val any) any {
	m, ok := val.(map[string]any)
	if !ok || m == nil {
		return val
	}

	mergeSchemaAllOf(m)
	simplifyNullableUnions(m)

	// const -> enum
	if cVal, hasConst := m["const"]; hasConst {
		if _, hasEnum := m["enum"]; !hasEnum {
			m["enum"] = []any{cVal}
		}
		delete(m, "const")
	}

	// type: ["string", "null"] -> "string"
	if rawType, ok := m["type"].([]any); ok {
		chosen := ""
		for _, item := range rawType {
			s := strings.ToLower(strings.TrimSpace(fmt.Sprint(item)))
			if s != "" && s != "null" {
				chosen = s
				break
			}
		}
		if chosen == "" {
			chosen = "string"
		}
		m["type"] = chosen
	}

	// 仅保留标准支持的 format
	if fVal, ok := m["format"].(string); ok {
		switch strings.ToLower(strings.TrimSpace(fVal)) {
		case "enum", "date-time":
			// keep
		default:
			delete(m, "format")
		}
	}

	for k := range unsupportedSchemaKeys {
		delete(m, k)
	}

	// 递归清理 properties
	var validPropKeys map[string]struct{}
	if props, ok := m["properties"].(map[string]any); ok {
		validPropKeys = make(map[string]struct{}, len(props))
		for pk, pv := range props {
			props[pk] = cleanSchemaNode(pv)
			validPropKeys[pk] = struct{}{}
		}
	}

	// 递归清理 items
	if items, ok := m["items"]; ok {
		if itemArr, isArr := items.([]any); isArr && len(itemArr) > 0 {
			m["items"] = cleanSchemaNode(itemArr[0])
		} else {
			m["items"] = cleanSchemaNode(items)
		}
	}

	// 过滤 required：仅保留 properties 中真实存在的字段，且去重
	if reqRaw, ok := m["required"].([]any); ok {
		if len(validPropKeys) == 0 {
			delete(m, "required")
		} else {
			seen := make(map[string]struct{}, len(reqRaw))
			filtered := make([]any, 0, len(reqRaw))
			for _, item := range reqRaw {
				name, ok := item.(string)
				if !ok || name == "" {
					continue
				}
				if _, exists := validPropKeys[name]; !exists {
					continue
				}
				if _, dup := seen[name]; dup {
					continue
				}
				seen[name] = struct{}{}
				filtered = append(filtered, name)
			}
			if len(filtered) > 0 {
				m["required"] = filtered
			} else {
				delete(m, "required")
			}
		}
	}

	// 若 enum 值包含非字符串，统一转为字符串以兼容 Gemini FunctionDeclaration
	if enumRaw, ok := m["enum"].([]any); ok {
		strEnum := make([]any, 0, len(enumRaw))
		for _, item := range enumRaw {
			if item == nil {
				continue
			}
			strEnum = append(strEnum, fmt.Sprint(item))
		}
		if len(strEnum) > 0 {
			m["enum"] = strEnum
			if _, hasType := m["type"]; !hasType {
				m["type"] = "string"
			}
		} else {
			delete(m, "enum")
		}
	}

	return m
}

func mergeSchemaAllOf(m map[string]any) {
	allOf, ok := m["allOf"].([]any)
	if !ok || len(allOf) == 0 {
		return
	}
	delete(m, "allOf")

	props, _ := m["properties"].(map[string]any)
	var reqList []any
	if existingReq, ok := m["required"].([]any); ok {
		reqList = append(reqList, existingReq...)
	}

	for _, branch := range allOf {
		bm, ok := branch.(map[string]any)
		if !ok || bm == nil {
			continue
		}
		mergeSchemaAllOf(bm)
		if bProps, ok := bm["properties"].(map[string]any); ok {
			if props == nil {
				props = make(map[string]any, len(bProps))
			}
			for k, v := range bProps {
				if _, exists := props[k]; !exists {
					props[k] = v
				}
			}
		}
		if bReq, ok := bm["required"].([]any); ok {
			reqList = append(reqList, bReq...)
		}
		if _, hasType := m["type"]; !hasType {
			if bt, ok := bm["type"]; ok {
				m["type"] = bt
			}
		}
		if _, hasDesc := m["description"]; !hasDesc {
			if bd, ok := bm["description"]; ok {
				m["description"] = bd
			}
		}
	}

	if props != nil {
		m["properties"] = props
		if _, hasType := m["type"]; !hasType {
			m["type"] = "object"
		}
	}
	if len(reqList) > 0 {
		m["required"] = reqList
	}
}

func simplifyNullableUnions(m map[string]any) {
	for _, unionKey := range []string{"anyOf", "oneOf"} {
		branches, ok := m[unionKey].([]any)
		if !ok || len(branches) == 0 {
			continue
		}
		var nonNull []map[string]any
		for _, b := range branches {
			bm, ok := b.(map[string]any)
			if !ok || bm == nil {
				continue
			}
			if t, _ := bm["type"].(string); strings.EqualFold(strings.TrimSpace(t), "null") {
				continue
			}
			nonNull = append(nonNull, bm)
		}
		delete(m, unionKey)
		if len(nonNull) > 0 {
			chosen := nonNull[0]
			for k, v := range chosen {
				if _, exists := m[k]; !exists {
					m[k] = v
				}
			}
		}
	}
}
