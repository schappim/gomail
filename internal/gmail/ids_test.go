package gmail

import (
	"strings"
	"testing"
)

func TestParseAndFormatID(t *testing.T) {
	const big uint64 = 1278455344230334865 // > uint32, as real Gmail ids are
	if got := FormatID(big); got != "11bdfc5cae0c8191" {
		t.Fatalf("FormatID(%d) = %q", big, got)
	}

	valid := map[string]uint64{
		"11bdfc5cae0c8191":   big,
		"11BDFC5CAE0C8191":   big,
		"0x11bdfc5cae0c8191": big,
		"0X11BDFC5CAE0C8191": big,
		"  11bdfc5cae0c8191": big,
		"ffffffffffffffff":   ^uint64(0),
		"1":                  1,
		"18c2f4e5a6b7c8d9":   0x18c2f4e5a6b7c8d9,
	}
	for in, want := range valid {
		got, err := ParseID(in)
		if err != nil || got != want {
			t.Errorf("ParseID(%q) = %d, %v; want %d", in, got, err, want)
		}
	}

	for _, in := range []string{"", "0x", "0", "xyz", "-1", "12345678901234567", "FMfcgzGxyzABC", "11bd fc5c", "0x0x1"} {
		if v, err := ParseID(in); err == nil {
			t.Errorf("ParseID(%q) = %d, want an error", in, v)
		} else if !strings.Contains(err.Error(), "invalid Gmail id") {
			t.Errorf("ParseID(%q) error %q lacks context", in, err)
		}
	}

	for _, v := range []uint64{1, 0xdeadbeef, big, ^uint64(0)} {
		got, err := ParseID(FormatID(v))
		if err != nil || got != v {
			t.Errorf("round trip %d: got %d, %v", v, got, err)
		}
	}
}
