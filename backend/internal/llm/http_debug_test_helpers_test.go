package llm

func buildHTTPDebugRequestDiagnosticsWithBody(protocol, profile string, requestBody map[string]interface{}, bodyBytes []byte) map[string]interface{} {
	rawFields, ok := parseHTTPDebugRawFields(bodyBytes)
	if !ok {
		return buildHTTPDebugRequestDiagnostics(protocol, profile, requestBody)
	}
	return buildHTTPDebugRequestDiagnosticsWithDigest(protocol, profile, requestBody, func(key string, value interface{}) string {
		if value == nil {
			return ""
		}
		if raw, ok := rawFields[key]; ok {
			return sha256HexJSONValue(raw)
		}
		return canonicalHTTPDebugValueSHA256(value)
	})
}
