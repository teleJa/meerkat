package androidclient

import (
	"encoding/base64"
	"fmt"
	"strings"
)

// EncodePathParam encodes a value for use as one path segment. The Android
// WebView asset loader drops query strings and request bodies for non-/wails/
// paths, so the client passes parameters as base64url path segments instead.
func EncodePathParam(value string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(value))
}

// DecodePathParam reverses EncodePathParam for the last segment after prefix.
func DecodePathParam(prefix, path string) (string, error) {
	raw := strings.TrimPrefix(path, prefix)
	raw = strings.TrimPrefix(raw, "/")
	if raw == "" {
		return "", fmt.Errorf("missing parameter")
	}
	decoded, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return "", fmt.Errorf("invalid parameter: %w", err)
	}
	return string(decoded), nil
}
