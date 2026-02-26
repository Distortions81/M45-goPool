package main

import (
	"encoding/json"
	"fmt"
)

func (s *StatusServer) buildAdminLoadedConfigOverridesJSON() (string, error) {
	cfg := Config{}
	if s != nil {
		cfg = s.Config()
	}
	base := defaultConfig()

	// Avoid false positives from generated defaults.
	base.PoolEntropy = cfg.PoolEntropy

	curMap, err := effectiveConfigToMap(cfg.Effective())
	if err != nil {
		return "", err
	}
	baseMap, err := effectiveConfigToMap(base.Effective())
	if err != nil {
		return "", err
	}

	diff := diffConfigMaps(curMap, baseMap)
	if diff == nil {
		diff = map[string]any{}
	}
	out, err := json.MarshalIndent(diff, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshal config overrides: %w", err)
	}
	return string(out), nil
}

func effectiveConfigToMap(v EffectiveConfig) (map[string]any, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("marshal effective config: %w", err)
	}
	out := map[string]any{}
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, fmt.Errorf("unmarshal effective config: %w", err)
	}
	return out, nil
}

func diffConfigMaps(cur, base map[string]any) map[string]any {
	if len(cur) == 0 {
		return nil
	}
	out := make(map[string]any)
	for k, v := range cur {
		bv, ok := base[k]
		if !ok {
			out[k] = v
			continue
		}
		if nested, ok := diffConfigValues(v, bv); ok {
			out[k] = nested
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func diffConfigValues(cur, base any) (any, bool) {
	curMap, curIsMap := cur.(map[string]any)
	baseMap, baseIsMap := base.(map[string]any)
	if curIsMap && baseIsMap {
		diff := diffConfigMaps(curMap, baseMap)
		if diff == nil {
			return nil, false
		}
		return diff, true
	}
	if valuesEqualJSON(cur, base) {
		return nil, false
	}
	return cur, true
}

func valuesEqualJSON(a, b any) bool {
	ab, errA := json.Marshal(a)
	bb, errB := json.Marshal(b)
	if errA != nil || errB != nil {
		return fmt.Sprint(a) == fmt.Sprint(b)
	}
	return string(ab) == string(bb)
}
