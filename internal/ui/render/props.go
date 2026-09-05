package render

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

type propMap map[string]any

type pair struct {
	Key   string
	Value string
}

type tableColumn struct {
	ID    string
	Title string
}

func parseProps(raw json.RawMessage) propMap {
	if len(raw) == 0 || string(raw) == "null" {
		return propMap{}
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return propMap{"text": string(raw)}
	}
	return propMap(m)
}

func (p propMap) String(key string) string {
	return sanitize(valueString(p[key]))
}

func (p propMap) StringDefault(key, fallback string) string {
	if v := p.String(key); v != "" {
		return v
	}
	return fallback
}

func (p propMap) First(keys ...string) string {
	for _, key := range keys {
		if v := p.String(key); v != "" {
			return v
		}
	}
	return ""
}

func (p propMap) Bool(key string) bool {
	return p.BoolDefault(key, false)
}

func (p propMap) BoolDefault(key string, fallback bool) bool {
	switch v := p[key].(type) {
	case bool:
		return v
	case string:
		parsed, err := strconv.ParseBool(v)
		if err == nil {
			return parsed
		}
	case float64:
		return v != 0
	case int:
		return v != 0
	}
	return fallback
}

func (p propMap) Int(key string) int {
	return p.IntDefault(key, 0)
}

func (p propMap) IntDefault(key string, fallback int) int {
	switch v := p[key].(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	case json.Number:
		parsed, err := v.Int64()
		if err == nil {
			return int(parsed)
		}
	case string:
		parsed, err := strconv.Atoi(v)
		if err == nil {
			return parsed
		}
	}
	return fallback
}

func (p propMap) Has(key string) bool {
	_, ok := p[key]
	return ok
}

func (p propMap) FloatDefault(key string, fallback float64) float64 {
	switch v := p[key].(type) {
	case float64:
		return v
	case float32:
		return float64(v)
	case int:
		return float64(v)
	case int64:
		return float64(v)
	case json.Number:
		parsed, err := v.Float64()
		if err == nil {
			return parsed
		}
	case string:
		parsed, err := strconv.ParseFloat(v, 64)
		if err == nil {
			return parsed
		}
	}
	return fallback
}

func (p propMap) Array(key string) []any {
	switch v := p[key].(type) {
	case []any:
		return v
	case []string:
		out := make([]any, len(v))
		for i := range v {
			out[i] = v[i]
		}
		return out
	case nil:
		return nil
	default:
		return []any{v}
	}
}

func (p propMap) Strings(key string) []string {
	items := p.Array(key)
	out := make([]string, 0, len(items))
	for _, item := range items {
		switch v := item.(type) {
		case map[string]any:
			out = append(out, sanitize(firstMapString(v, "label", "title", "name", "value", "text", "id")))
		default:
			out = append(out, sanitize(valueString(v)))
		}
	}
	return nonEmpty(out)
}

func (p propMap) Floats(key string) []float64 {
	items := p.Array(key)
	out := make([]float64, 0, len(items))
	for _, item := range items {
		switch v := item.(type) {
		case float64:
			out = append(out, v)
		case int:
			out = append(out, float64(v))
		case string:
			parsed, err := strconv.ParseFloat(v, 64)
			if err == nil {
				out = append(out, parsed)
			}
		}
	}
	return out
}

func (p propMap) Pairs() []pair {
	for _, key := range []string{"pairs", "items", "entries", "fields"} {
		items := p.Array(key)
		if len(items) == 0 {
			continue
		}
		out := make([]pair, 0, len(items))
		for _, item := range items {
			switch v := item.(type) {
			case map[string]any:
				k := firstMapString(v, "key", "label", "name", "title")
				val := firstMapString(v, "value", "text", "description", "status")
				if k != "" || val != "" {
					out = append(out, pair{k, val})
				}
			case []any:
				if len(v) >= 2 {
					out = append(out, pair{valueString(v[0]), valueString(v[1])})
				}
			default:
				text := valueString(v)
				if text != "" {
					out = append(out, pair{text, ""})
				}
			}
		}
		if len(out) > 0 {
			return out
		}
	}
	return nil
}

func (p propMap) TableColumns() []tableColumn {
	items := p.Array("columns")
	columns := make([]tableColumn, 0, len(items))
	for i, item := range items {
		col, ok := tableColumnFromValue(item, i)
		if ok {
			columns = append(columns, col)
		}
	}
	return columns
}

func tableColumnFromValue(value any, index int) (tableColumn, bool) {
	switch v := value.(type) {
	case map[string]any:
		id := sanitize(firstMapString(v, "id", "key", "field", "accessor", "name"))
		title := sanitize(firstMapString(v, "title", "label", "header", "name", "text", "value", "id", "key", "field", "accessor"))
		if id == "" {
			id = title
		}
		if title == "" {
			title = id
		}
		return tableColumn{ID: id, Title: title}, id != "" || title != ""
	default:
		text := sanitize(valueString(v))
		if text == "" {
			return tableColumn{}, false
		}
		return tableColumn{ID: text, Title: text}, true
	case nil:
		return tableColumn{ID: strconv.Itoa(index), Title: strconv.Itoa(index)}, true
	}
}

func (p propMap) TableRows(columns []tableColumn) []map[string]string {
	items := p.Array("rows")
	if len(items) == 0 {
		items = p.Array("items")
	}
	rows := make([]map[string]string, 0, len(items))
	for _, item := range items {
		switch v := item.(type) {
		case map[string]any:
			if cells, ok := firstPresent(v, "cells", "values"); ok {
				if row := tableRowFromCells(cells, columns); len(row) > 0 {
					rows = append(rows, row)
				}
				continue
			}
			row := make(map[string]string, len(v))
			for key, value := range v {
				row[key] = cellString(value)
			}
			rows = append(rows, row)
		case []any:
			rows = append(rows, tableRowFromCells(v, columns))
		default:
			if text := cellString(v); text != "" {
				rows = append(rows, map[string]string{"value": text})
			}
		}
	}
	return rows
}

func tableRowFromCells(cells any, columns []tableColumn) map[string]string {
	switch v := cells.(type) {
	case map[string]any:
		row := make(map[string]string, len(v))
		for key, cell := range v {
			row[key] = cellString(cell)
		}
		return row
	case []any:
		row := make(map[string]string, len(v))
		for i, cell := range v {
			key := strconv.Itoa(i)
			if i < len(columns) && columns[i].ID != "" {
				key = columns[i].ID
			}
			row[key] = cellString(cell)
		}
		return row
	default:
		if text := cellString(v); text != "" {
			return map[string]string{"value": text}
		}
		return nil
	}
}

func firstPresent(m map[string]any, keys ...string) (any, bool) {
	for _, key := range keys {
		if value, ok := m[key]; ok {
			return value, true
		}
	}
	return nil, false
}

func cellString(value any) string {
	return sanitize(cellDisplayString(value, true))
}

func cellDisplayString(value any, allowJSON bool) string {
	switch v := value.(type) {
	case nil:
		return ""
	case map[string]any:
		for _, key := range []string{"display", "render", "view"} {
			if display, ok := v[key]; ok {
				if text := cellDisplayString(display, false); text != "" {
					return text
				}
			}
		}
		for _, key := range []string{"plain", "fallback", "formatted", "text", "label", "title", "value", "content", "message", "alt"} {
			if item, ok := v[key]; ok {
				if text := cellDisplayString(item, false); text != "" {
					return text
				}
			}
		}
		if props, ok := v["props"].(map[string]any); ok {
			if text := cellDisplayString(props, false); text != "" {
				return text
			}
		}
		if children, ok := firstPresent(v, "children", "items"); ok {
			if text := cellDisplayString(children, false); text != "" {
				return text
			}
		}
		for _, key := range []string{"name", "id"} {
			if item, ok := v[key]; ok {
				if text := cellDisplayString(item, false); text != "" {
					return text
				}
			}
		}
		if !allowJSON {
			return ""
		}
		return valueString(v)
	case []any:
		parts := make([]string, 0, len(v))
		for _, item := range v {
			if text := cellDisplayString(item, false); text != "" {
				parts = append(parts, text)
			}
		}
		return strings.Join(nonEmpty(parts), ", ")
	default:
		return valueString(v)
	}
}

func valueString(value any) string {
	switch v := value.(type) {
	case nil:
		return ""
	case string:
		return v
	case fmt.Stringer:
		return v.String()
	case float64:
		if v == float64(int64(v)) {
			return strconv.FormatInt(int64(v), 10)
		}
		return strconv.FormatFloat(v, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(v)
	case []any:
		parts := make([]string, 0, len(v))
		for _, item := range v {
			parts = append(parts, valueString(item))
		}
		return strings.Join(nonEmpty(parts), ", ")
	case map[string]any:
		if text := firstMapString(v, "label", "title", "name", "value", "text", "id", "message"); text != "" {
			return text
		}
		data, _ := json.Marshal(v)
		return string(data)
	default:
		return fmt.Sprint(v)
	}
}

func firstMapString(m map[string]any, keys ...string) string {
	for _, key := range keys {
		if text := valueString(m[key]); text != "" {
			return text
		}
	}
	return ""
}
