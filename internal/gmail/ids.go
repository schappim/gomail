package gmail

import (
	"fmt"
	"strconv"
	"strings"
)

// ParseID accepts a Gmail id in hex (as shown in the Gmail web UI) and returns its numeric value.
// Matching is case-insensitive and an optional 0x prefix is allowed.
func ParseID(s string) (uint64, error) {
	t := strings.TrimSpace(s)
	if len(t) > 2 && t[0] == '0' && (t[1] == 'x' || t[1] == 'X') {
		t = t[2:]
	}
	if t == "" || len(t) > 16 || !isHex(t) {
		return 0, fmt.Errorf("invalid Gmail id %q: want up to 16 hex digits, e.g. 18c2f4e5a6b7c8d9 (as shown in the Gmail web UI)", s)
	}
	v, err := strconv.ParseUint(t, 16, 64)
	if err != nil || v == 0 {
		return 0, fmt.Errorf("invalid Gmail id %q", s)
	}
	return v, nil
}

// FormatID renders a numeric Gmail id as lowercase hex.
func FormatID(v uint64) string { return strconv.FormatUint(v, 16) }

func isHex(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') && (c < 'A' || c > 'F') {
			return false
		}
	}
	return true
}

// parseIDs parses every id, reporting all invalid ones at once.
func parseIDs(ids []string) ([]uint64, error) {
	if len(ids) == 0 {
		return nil, fmt.Errorf("no message ids given")
	}
	nums := make([]uint64, 0, len(ids))
	for _, id := range ids {
		n, err := ParseID(id)
		if err != nil {
			return nil, err
		}
		nums = append(nums, n)
	}
	return nums, nil
}
