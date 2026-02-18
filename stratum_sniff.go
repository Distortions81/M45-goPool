package main

import (
	"bytes"
	"strconv"
)

func sniffStratumMethodID(data []byte) (string, any, bool) {
	idIdx := bytes.Index(data, []byte(`"id"`))
	if idIdx < 0 {
		return "", nil, false
	}
	idStart, ok := findValueStart(data, idIdx+len(`"id"`))
	if !ok {
		return "", nil, false
	}
	idVal, _, ok := parseJSONValue(data, idStart)
	if !ok {
		return "", nil, false
	}

	methodIdx := bytes.Index(data, []byte(`"method"`))
	if methodIdx < 0 {
		return "", nil, false
	}
	methodStart, ok := findValueStart(data, methodIdx+len(`"method"`))
	if !ok {
		return "", nil, false
	}
	if methodStart >= len(data) || data[methodStart] != '"' {
		return "", nil, false
	}
	methodStart++
	methodEnd := methodStart
	for methodEnd < len(data) {
		switch data[methodEnd] {
		case '\\':
			methodEnd += 2
			continue
		case '"':
			method := string(data[methodStart:methodEnd])
			return method, idVal, true
		default:
			methodEnd++
		}
	}
	return "", nil, false
}

func findValueStart(data []byte, idx int) (int, bool) {
	for idx < len(data) && data[idx] != ':' {
		idx++
	}
	if idx >= len(data) {
		return 0, false
	}
	idx++
	for idx < len(data) {
		switch data[idx] {
		case ' ', '\t', '\n', '\r':
			idx++
			continue
		default:
			return idx, true
		}
	}
	return 0, false
}

func sniffStratumStringParams(data []byte, limit int) ([]string, bool) {
	if limit <= 0 {
		return nil, false
	}
	idx := bytes.Index(data, []byte(`"params"`))
	if idx < 0 {
		return nil, false
	}
	start, ok := findValueStart(data, idx+len(`"params"`))
	if !ok || start >= len(data) || data[start] != '[' {
		return nil, false
	}

	i := start + 1
	params := make([]string, 0, limit)
	for i < len(data) {
		i = skipSpaces(data, i)
		if i >= len(data) {
			return nil, false
		}
		switch data[i] {
		case ']':
			return params, true
		case ',':
			i++
			continue
		case '"':
			j := i + 1
			hasEscape := false
			for j < len(data) {
				if data[j] == '\\' {
					hasEscape = true
					j += 2
					continue
				}
				if data[j] == '"' {
					break
				}
				j++
			}
			if j >= len(data) {
				return nil, false
			}
			var val string
			if !hasEscape {
				val = string(data[i+1 : j])
			} else {
				decoded, err := strconv.Unquote(string(data[i : j+1]))
				if err != nil {
					return nil, false
				}
				val = decoded
			}
			params = append(params, val)
			if len(params) >= limit {
				return params, true
			}
			i = j + 1
		default:
			return nil, false
		}
	}
	return nil, false
}

func sniffStratumSubmitParamsBytes(data []byte) (worker, jobID, extranonce2, ntime, nonce, version []byte, haveVersion bool, ok bool) {
	// mining.submit params are typically 5 or 6 JSON strings:
	// [worker_name, job_id, extranonce2, ntime, nonce, (optional) version]
	idx := bytes.Index(data, []byte(`"params"`))
	if idx < 0 {
		return nil, nil, nil, nil, nil, nil, false, false
	}
	start, ok := findValueStart(data, idx+len(`"params"`))
	if !ok || start >= len(data) || data[start] != '[' {
		return nil, nil, nil, nil, nil, nil, false, false
	}

	i := start + 1
	var fields [6][]byte
	n := 0
	for i < len(data) && n < len(fields) {
		i = skipSpaces(data, i)
		if i >= len(data) {
			return nil, nil, nil, nil, nil, nil, false, false
		}
		switch data[i] {
		case ']':
			if n == 5 {
				return fields[0], fields[1], fields[2], fields[3], fields[4], nil, false, true
			}
			if n == 6 {
				return fields[0], fields[1], fields[2], fields[3], fields[4], fields[5], true, true
			}
			return nil, nil, nil, nil, nil, nil, false, false
		case ',':
			i++
			continue
		case '"':
			j := i + 1
			for j < len(data) {
				if data[j] == '\\' {
					// Escapes require unquoting; fall back to the full decoder path.
					return nil, nil, nil, nil, nil, nil, false, false
				}
				if data[j] == '"' {
					break
				}
				j++
			}
			if j >= len(data) {
				return nil, nil, nil, nil, nil, nil, false, false
			}
			fields[n] = data[i+1 : j]
			n++
			i = j + 1
		default:
			return nil, nil, nil, nil, nil, nil, false, false
		}
	}

	// Parse didn't terminate cleanly.
	return nil, nil, nil, nil, nil, nil, false, false
}

func skipSpaces(data []byte, idx int) int {
	for idx < len(data) {
		switch data[idx] {
		case ' ', '\t', '\n', '\r':
			idx++
			continue
		default:
			return idx
		}
	}
	return idx
}

func parseJSONValue(data []byte, idx int) (any, int, bool) {
	if idx >= len(data) {
		return nil, idx, false
	}
	switch data[idx] {
	case '"':
		i := idx + 1
		for i < len(data) {
			if data[i] == '\\' {
				i++
				if i >= len(data) {
					return nil, idx, false
				}
			} else if data[i] == '"' {
				str, err := strconv.Unquote(string(data[idx : i+1]))
				if err != nil {
					return nil, idx, false
				}
				return str, i + 1, true
			}
			i++
		}
		return nil, idx, false
	case 'n':
		if len(data) >= idx+4 && string(data[idx:idx+4]) == "null" {
			return nil, idx + 4, true
		}
	case 't':
		if len(data) >= idx+4 && string(data[idx:idx+4]) == "true" {
			return true, idx + 4, true
		}
	case 'f':
		if len(data) >= idx+5 && string(data[idx:idx+5]) == "false" {
			return false, idx + 5, true
		}
	default:
		if data[idx] == '-' || (data[idx] >= '0' && data[idx] <= '9') {
			val, next, ok := parseInt64(data, idx)
			if !ok {
				return nil, idx, false
			}
			return val, next, true
		}
	}
	return nil, idx, false
}

func parseInt64(data []byte, idx int) (int64, int, bool) {
	if idx >= len(data) {
		return 0, idx, false
	}
	sign := int64(1)
	if data[idx] == '-' {
		sign = -1
		idx++
	}
	start := idx
	var val int64
	for idx < len(data) && data[idx] >= '0' && data[idx] <= '9' {
		val = val*10 + int64(data[idx]-'0')
		idx++
	}
	if idx == start {
		return 0, idx, false
	}
	return val * sign, idx, true
}
