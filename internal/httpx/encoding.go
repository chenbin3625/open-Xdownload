package httpx

import (
	"strconv"
	"strings"
)

// Supported content-encoding names served by this application.
const (
	EncodingBrotli = "br"
	EncodingGzip   = "gzip"
)

// NegotiateEncoding picks the client's preferred encoding from Accept-Encoding.
//
// supported lists the server's encodings in server preference order; the first
// one the client accepts (q>0) wins. A missing header, or one where every
// candidate is excluded with q=0, yields "" meaning "serve identity".
//
// "*" is honored as a wildcard for encodings that were not mentioned (and not
// explicitly excluded). Names are matched case-insensitively per RFC 9110.
func NegotiateEncoding(acceptEncoding string, supported ...string) string {
	accepted := parseAcceptEncoding(acceptEncoding)
	star := accepted["*"]
	for _, encoding := range supported {
		if allowed, ok := accepted[encoding]; ok {
			if allowed {
				return encoding
			}
			continue
		}
		if star {
			return encoding
		}
	}
	return ""
}

// parseAcceptEncoding returns advertised encodings. q>0 → true, q=0 → false
// (kept as an explicit exclusion so "*" cannot revive a rejected encoding).
func parseAcceptEncoding(header string) map[string]bool {
	accepted := make(map[string]bool)
	for _, part := range strings.Split(header, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		name, quality := part, 1.0
		if separator := strings.IndexByte(part, ';'); separator >= 0 {
			name = strings.TrimSpace(part[:separator])
			quality = parseQuality(part[separator+1:])
		}
		name = strings.ToLower(name)
		if name == "" {
			continue
		}
		accepted[name] = quality > 0
	}
	return accepted
}

func parseQuality(parameter string) float64 {
	parameter = strings.TrimSpace(parameter)
	if !strings.HasPrefix(parameter, "q=") {
		return 1.0
	}
	value, err := strconv.ParseFloat(strings.TrimPrefix(parameter, "q="), 64)
	if err != nil || value < 0 || value > 1 {
		return 1.0
	}
	return value
}
