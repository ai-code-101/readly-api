package account

import "testing"

func TestNormalizePhone(t *testing.T) {
	ok := map[string]string{
		"0743410697":       "254743410697",
		"0743 410 697":     "254743410697",
		"+254 743 410 697": "254743410697",
		"254743410697":     "254743410697",
		"743410697":        "254743410697",
		"0110123456":       "254110123456",
	}
	for in, want := range ok {
		if got, err := NormalizePhone(in); err != nil || got != want {
			t.Errorf("NormalizePhone(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "12345", "0612345678", "25474341069", "+1 415 555 0100"} {
		if _, err := NormalizePhone(bad); err == nil {
			t.Errorf("NormalizePhone(%q) accepted", bad)
		}
	}
}

func TestMask(t *testing.T) {
	if Mask("254743410697") != "2547•••••697" {
		t.Fatalf("mask = %q", Mask("254743410697"))
	}
}
