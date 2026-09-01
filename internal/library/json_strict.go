package library

import (
	"bytes"
	"encoding/json"
	"io"
	"unicode/utf8"
)

func validateJSONStructure(data []byte) error {
	if !utf8.Valid(data) {
		return contractError("JSON_NOT_UTF8")
	}
	if bytes.HasPrefix(data, []byte{0xef, 0xbb, 0xbf}) {
		return contractError("UTF8_BOM_FORBIDDEN")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := validateJSONValue(decoder); err != nil {
		return err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return contractError("INVALID_JSON")
	}
	return nil
}

func validateJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return contractError("INVALID_JSON")
	}
	delimiter, isDelimiter := token.(json.Delim)
	if !isDelimiter {
		return nil
	}
	switch delimiter {
	case '{':
		seen := make(map[string]struct{})
		for decoder.More() {
			keyToken, keyErr := decoder.Token()
			key, valid := keyToken.(string)
			if keyErr != nil || !valid {
				return contractError("INVALID_JSON")
			}
			if _, duplicate := seen[key]; duplicate {
				return contractError("DUPLICATE_JSON_KEY")
			}
			seen[key] = struct{}{}
			if err := validateJSONValue(decoder); err != nil {
				return err
			}
		}
		return consumeDelimiter(decoder, '}')
	case '[':
		for decoder.More() {
			if err := validateJSONValue(decoder); err != nil {
				return err
			}
		}
		return consumeDelimiter(decoder, ']')
	default:
		return contractError("INVALID_JSON")
	}
}

func consumeDelimiter(decoder *json.Decoder, expected json.Delim) error {
	token, err := decoder.Token()
	if err != nil || token != expected {
		return contractError("INVALID_JSON")
	}
	return nil
}

func decodeStrictJSON(data []byte, target any) error {
	if err := validateJSONStructure(data); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return contractError("INVALID_JSON")
	}
	if _, err := decoder.Token(); err != io.EOF {
		return contractError("INVALID_JSON")
	}
	return nil
}
