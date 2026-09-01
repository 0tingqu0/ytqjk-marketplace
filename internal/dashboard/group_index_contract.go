package dashboard

import (
	"bytes"
	"encoding/json"
	"io"
	"unicode/utf8"
)

func decodeGroupIndexPreviewRequest(data []byte) (groupIndexPreviewRequest, string) {
	if code := validateGroupIndexJSON(data); code != "" {
		return groupIndexPreviewRequest{}, code
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil || fields == nil || len(fields) != 2 {
		return groupIndexPreviewRequest{}, "INVALID_REQUEST_FIELDS"
	}
	nodeRaw, nodeFound := fields["node_id"]
	documentsRaw, documentsFound := fields["document_ids"]
	if !nodeFound || !documentsFound || isJSONNull(nodeRaw) || isJSONNull(documentsRaw) {
		return groupIndexPreviewRequest{}, "INVALID_REQUEST_FIELDS"
	}
	var request groupIndexPreviewRequest
	if err := json.Unmarshal(nodeRaw, &request.NodeID); err != nil || !safeIdentifier(request.NodeID) {
		return groupIndexPreviewRequest{}, "INVALID_REQUEST_FIELDS"
	}
	if err := json.Unmarshal(documentsRaw, &request.DocumentIDs); err != nil || request.DocumentIDs == nil {
		return groupIndexPreviewRequest{}, "INVALID_REQUEST_FIELDS"
	}
	return request, ""
}

func validateGroupIndexJSON(data []byte) string {
	if !utf8.Valid(data) || bytes.HasPrefix(data, []byte{0xef, 0xbb, 0xbf}) {
		return "INVALID_REQUEST_FIELDS"
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if code := validateGroupIndexJSONValue(decoder); code != "" {
		return code
	}
	if _, err := decoder.Token(); err != io.EOF {
		return "INVALID_REQUEST_FIELDS"
	}
	return ""
}

func validateGroupIndexJSONValue(decoder *json.Decoder) string {
	token, err := decoder.Token()
	if err != nil {
		return "INVALID_REQUEST_FIELDS"
	}
	delimiter, isDelimiter := token.(json.Delim)
	if !isDelimiter {
		return ""
	}
	switch delimiter {
	case '{':
		seen := make(map[string]struct{})
		for decoder.More() {
			keyToken, keyErr := decoder.Token()
			key, valid := keyToken.(string)
			if keyErr != nil || !valid {
				return "INVALID_REQUEST_FIELDS"
			}
			if _, duplicate := seen[key]; duplicate {
				return "DUPLICATE_JSON_KEY"
			}
			seen[key] = struct{}{}
			if code := validateGroupIndexJSONValue(decoder); code != "" {
				return code
			}
		}
		return consumeGroupIndexDelimiter(decoder, '}')
	case '[':
		for decoder.More() {
			if code := validateGroupIndexJSONValue(decoder); code != "" {
				return code
			}
		}
		return consumeGroupIndexDelimiter(decoder, ']')
	default:
		return "INVALID_REQUEST_FIELDS"
	}
}

func consumeGroupIndexDelimiter(decoder *json.Decoder, expected json.Delim) string {
	token, err := decoder.Token()
	if err != nil || token != expected {
		return "INVALID_REQUEST_FIELDS"
	}
	return ""
}

func isJSONNull(value []byte) bool {
	return bytes.Equal(bytes.TrimSpace(value), []byte("null"))
}
