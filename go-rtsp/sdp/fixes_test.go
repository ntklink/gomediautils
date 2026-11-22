package sdp

import "testing"

func TestParseMLinePortRangeValidationAndSequence(t *testing.T) {
	var m Media
	if err := m.ParseMLine("video 49170/3 RTP/AVP 96"); err != nil {
		t.Fatalf("valid port range rejected: %v", err)
	}
	want := []uint16{49170, 49171, 49172}
	if len(m.Ports) != len(want) {
		t.Fatalf("ports=%v, want %v", m.Ports, want)
	}
	for i := range want {
		if m.Ports[i] != want[i] {
			t.Fatalf("ports[%d]=%d, want %d (all=%v)", i, m.Ports[i], want[i], m.Ports)
		}
	}

	badLines := []string{
		"video 49170/0 RTP/AVP 96",
		"video 49170/65 RTP/AVP 96",
		"video 49170/999999999 RTP/AVP 96",
		"video 65535/2 RTP/AVP 96",
	}
	for _, line := range badLines {
		var bad Media
		if err := bad.ParseMLine(line); err == nil {
			t.Errorf("expected error for %q, got ports=%v", line, bad.Ports)
		}
	}
}
