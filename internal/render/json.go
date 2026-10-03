package render

import "encoding/json"

func jsonMarshal(v any) ([]byte, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return []byte("null"), nil
	}
	return b, nil
}
