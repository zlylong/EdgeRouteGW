package tune

import "testing"

func TestParseEthtoolChannels(t *testing.T) {
	out := "Channel parameters for eth0:\nPre-set maximums:\nRX:\t\tn/a\nTX:\t\tn/a\nOther:\t\tn/a\nCombined:\t4\nCurrent hardware settings:\nRX:\t\tn/a\nTX:\t\tn/a\nOther:\t\tn/a\nCombined:\t1\n"
	c, err := ParseEthtoolChannels(out)
	if err != nil || c.MaxCombined != 4 || c.CurCombined != 1 {
		t.Fatalf("%+v %v", c, err)
	}
	if _, err := ParseEthtoolChannels("Cannot get device channel parameters: Operation not supported"); err == nil {
		t.Fatal("expected error")
	}
}

func TestCPUMask(t *testing.T) {
	for n, want := range map[int]string{1: "1", 4: "f", 8: "ff", 32: "ffffffff", 40: "ff,ffffffff", 64: "ffffffff,ffffffff"} {
		if got := CPUMask(n); got != want {
			t.Errorf("CPUMask(%d)=%q want %q", n, got, want)
		}
	}
	if normalizeMask("0000000f\n") != "f" || normalizeMask("00000000,0000000f") != "f" || normalizeMask("0") != "0" {
		t.Fatal("normalizeMask")
	}
}
