package main

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"
)

// ---------------------------------------------------------------------------
// API Key Detection & Masking
// ---------------------------------------------------------------------------

var hashSalt string

func initSalt() string {
	cfg := currentConfig()
	if cfg.APIKeyHashSalt != "" {
		return cfg.APIKeyHashSalt
	}
	if hashSalt == "" {
		b := make([]byte, 16)
		if _, err := rand.Read(b); err != nil {
			hashSalt = "usage-keeper-default-salt"
		} else {
			hashSalt = hex.EncodeToString(b)
		}
	}
	return hashSalt
}

func hashAPIKey(raw string) string {
	salt := initSalt()
	h := sha256.New224()
	h.Write([]byte(salt))
	h.Write([]byte(strings.TrimSpace(raw)))
	return hex.EncodeToString(h.Sum(nil))
}

func maskAPIKey(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}
	if len(s) <= 4 {
		return s[:1] + "******"
	}
	prefix := 2
	suffix := 2
	if len(s) < prefix+suffix {
		return s[:1] + "******" + s[len(s)-1:]
	}
	return s[:prefix] + "******" + s[len(s)-suffix:]
}

func looksLikeSecretKey(raw string) bool {
	s := strings.TrimSpace(raw)
	if s == "" {
		return false
	}
	if strings.HasPrefix(s, "sk-") || strings.HasPrefix(s, "AIza") ||
		strings.HasPrefix(s, "hf_") || strings.HasPrefix(s, "pk_") ||
		strings.HasPrefix(s, "rk_") {
		return true
	}
	if len(s) >= 40 && !strings.ContainsAny(s, " /.-_") {
		return true
	}
	if len(s) >= 80 && !strings.Contains(s, " ") {
		return true
	}
	return false
}

// ---------------------------------------------------------------------------
// Credential Stripping
// ---------------------------------------------------------------------------

func stripCredentialSuffix(raw string) string {
	value := strings.TrimSpace(raw)
	if value == "" {
		return ""
	}
	parts := splitBySeparators(value)
	for i, part := range parts {
		normalized := strings.ToLower(strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(part, "-", ""), "_", "")))
		if normalized == "apikey" || normalized == "key" || normalized == "credential" || normalized == "auth" {
			if i > 0 {
				return strings.Join(parts[:i], " \u00b7 ")
			}
		}
	}
	if len(parts) > 1 && looksLikeCredentialID(parts[len(parts)-1]) {
		return strings.Join(parts[:len(parts)-1], " \u00b7 ")
	}
	if len(parts) > 1 && looksLikeSecretKey(parts[len(parts)-1]) {
		return strings.Join(parts[:len(parts)-1], " \u00b7 ")
	}
	return value
}

func splitBySeparators(s string) []string {
	if strings.Contains(s, " \u00b7 ") {
		return strings.Split(s, " \u00b7 ")
	}
	if strings.Contains(s, " - ") {
		return strings.Split(s, " - ")
	}
	if strings.Contains(s, " | ") {
		return strings.Split(s, " | ")
	}
	if strings.Contains(s, "/") {
		return strings.Split(s, "/")
	}
	return []string{s}
}

func looksLikeCredentialID(raw string) bool {
	s := strings.TrimSpace(raw)
	if len(s) >= 8 {
		allHex := true
		for _, ch := range s {
			if !((ch >= '0' && ch <= '9') || (ch >= 'a' && ch <= 'f') || (ch >= 'A' && ch <= 'F')) {
				allHex = false
				break
			}
		}
		if allHex {
			return true
		}
	}
	return len(s) >= 32 && !strings.ContainsAny(s, " /.-_")
}

// ---------------------------------------------------------------------------
// Header Filtering
// ---------------------------------------------------------------------------

func isSensitiveHeader(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "authorization", "proxy-authorization", "cookie", "set-cookie",
		"x-api-key", "x-auth-token", "x-access-token", "api-key":
		return true
	default:
		return false
	}
}

type headerWhitelist struct {
	all      bool
	exact    map[string]bool
	prefixes []string
}

func parseHeaderWhitelist(raw string) headerWhitelist {
	set := headerWhitelist{exact: make(map[string]bool)}
	for _, name := range strings.Split(raw, ",") {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		name = strings.ToLower(name)
		if name == "*" {
			set.all = true
			continue
		}
		if strings.HasSuffix(name, "*") {
			prefix := strings.TrimSpace(strings.TrimSuffix(name, "*"))
			if prefix != "" {
				set.prefixes = append(set.prefixes, prefix)
			}
			continue
		}
		set.exact[name] = true
	}
	return set
}

func (w headerWhitelist) matches(name string) bool {
	normalized := strings.ToLower(strings.TrimSpace(name))
	if normalized == "" || isSensitiveHeader(normalized) {
		return false
	}
	if w.all || w.exact[normalized] {
		return true
	}
	for _, prefix := range w.prefixes {
		if strings.HasPrefix(normalized, prefix) {
			return true
		}
	}
	return false
}

func (w headerWhitelist) String() string {
	parts := make([]string, 0, len(w.exact)+len(w.prefixes)+1)
	if w.all {
		parts = append(parts, "*")
	}
	for name := range w.exact {
		parts = append(parts, name)
	}
	for _, prefix := range w.prefixes {
		parts = append(parts, prefix+"*")
	}
	sort.Strings(parts)
	return strings.Join(parts, ",")
}

func filterHeaders(headers map[string][]string, whitelist headerWhitelist) map[string][]string {
	if len(headers) == 0 {
		return nil
	}
	if !whitelist.all && len(whitelist.exact) == 0 && len(whitelist.prefixes) == 0 {
		return nil
	}
	out := make(map[string][]string)
	for k, v := range headers {
		if whitelist.matches(k) {
			copied := make([]string, len(v))
			copy(copied, v)
			out[k] = copied
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// ---------------------------------------------------------------------------
// Sensitive Text Redaction
// ---------------------------------------------------------------------------

const redactedMarker = "******"

func redactSensitiveText(value string) string {
	if value == "" {
		return ""
	}
	value = redactKeyPrefix(value, "sk-")
	value = redactKeyPrefix(value, "AIza")
	value = redactKeyPrefix(value, "hf_")
	value = redactKeyPrefix(value, "pk_")
	value = redactKeyPrefix(value, "rk_")
	value = redactAuthHeader(value, "Authorization:")
	value = redactAuthHeader(value, "authorization:")
	value = redactAuthHeader(value, "Bearer ")
	value = redactAuthHeader(value, "bearer ")
	value = redactAuthHeader(value, "X-API-Key:")
	value = redactAuthHeader(value, "x-api-key:")
	value = redactAuthHeader(value, "Api-Key:")
	value = redactAuthHeader(value, "api-key:")
	value = redactQueryParam(value, "key")
	value = redactQueryParam(value, "token")
	value = redactQueryParam(value, "api_key")
	value = redactQueryParam(value, "apikey")
	return value
}

func redactKeyPrefix(s, prefix string) string {
	result := s
	for {
		idx := strings.Index(result, prefix)
		if idx < 0 {
			break
		}
		end := strings.IndexFunc(result[idx+len(prefix):], func(r rune) bool {
			return !(r >= 'a' && r <= 'z') && !(r >= 'A' && r <= 'Z') && !(r >= '0' && r <= '9') && r != '-' && r != '_'
		})
		var token string
		if end < 0 {
			token = result[idx:]
		} else {
			token = result[idx : idx+len(prefix)+end]
		}
		result = strings.Replace(result, token, redactedMarker, 1)
	}
	return result
}

func redactAuthHeader(s, marker string) string {
	idx := strings.Index(s, marker)
	if idx < 0 {
		return s
	}
	start := idx + len(marker)
	end := strings.Index(s[start:], "\n")
	var value string
	if end < 0 {
		value = s[start:]
	} else {
		value = s[start : start+end]
	}
	value = strings.TrimSpace(value)
	if value == "" {
		return s
	}
	return s[:start] + redactedMarker + s[start+len(value):]
}

func redactQueryParam(s, param string) string {
	p := param + "="
	idx := strings.Index(strings.ToLower(s), p)
	if idx < 0 {
		return s
	}
	start := idx + len(p)
	end := strings.Index(s[start:], "&")
	var value string
	if end < 0 {
		value = s[start:]
	} else {
		value = s[start : start+end]
	}
	if value == "" {
		return s
	}
	return s[:start] + redactedMarker + s[start+len(value):]
}
