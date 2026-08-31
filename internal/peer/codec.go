package peer

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"sort"
)

func decodeExact(content []byte, target any, fields ...string) error {
	if err := rejectDuplicateJSONKeys(content); err != nil {
		return err
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(content, &raw); err != nil || raw == nil {
		return errors.New("INVALID_JSON_OBJECT")
	}
	actual := make([]string, 0, len(raw))
	for field := range raw {
		actual = append(actual, field)
	}
	sort.Strings(actual)
	expected := append([]string(nil), fields...)
	sort.Strings(expected)
	if len(actual) != len(expected) {
		return errors.New("INVALID_JSON_FIELDS")
	}
	for index := range actual {
		if actual[index] != expected[index] {
			return errors.New("INVALID_JSON_FIELDS")
		}
	}
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return errors.New("INVALID_JSON_TRAILING_DATA")
	}
	return nil
}

func rejectDuplicateJSONKeys(content []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.UseNumber()
	if err := consumeJSONValue(decoder); err != nil {
		return err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return errors.New("INVALID_JSON_TRAILING_DATA")
	}
	return nil
}

func consumeJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delimiter {
	case '{':
		seen := map[string]bool{}
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok || seen[key] {
				return errors.New("DUPLICATE_JSON_FIELD")
			}
			seen[key] = true
			if err := consumeJSONValue(decoder); err != nil {
				return err
			}
		}
		end, err := decoder.Token()
		if err != nil || end != json.Delim('}') {
			return errors.New("INVALID_JSON_OBJECT")
		}
	case '[':
		for decoder.More() {
			if err := consumeJSONValue(decoder); err != nil {
				return err
			}
		}
		end, err := decoder.Token()
		if err != nil || end != json.Delim(']') {
			return errors.New("INVALID_JSON_ARRAY")
		}
	default:
		return errors.New("INVALID_JSON_DELIMITER")
	}
	return nil
}
