package wav

import (
	"bytes"
	"io"
	"testing"
)

func TestFormatValidateOverflow(t *testing.T) {
	// BlockAlign = 65535 * 2 = 131070 > 0xFFFF
	if _, err := NewWriter(io.Discard, Format{
		AudioFormat:   FormatPCM,
		Channels:      65535,
		SampleRate:    8000,
		BitsPerSample: 16,
	}); err == nil {
		t.Fatalf("expected error for BlockAlign > 0xFFFF")
	}

	// ByteRate = 1_000_000 * (2000 * 4) = 8_000_000_000 > 0xFFFFFFFF
	if _, err := NewWriter(io.Discard, Format{
		AudioFormat:   FormatPCM,
		Channels:      2000,
		SampleRate:    1000000,
		BitsPerSample: 32,
	}); err == nil {
		t.Fatalf("expected error for ByteRate > 0xFFFFFFFF")
	}
}

func TestReaderDurationStableAfterRead(t *testing.T) {
	ws := &memWriteSeeker{}
	f := Format{AudioFormat: FormatPCM, Channels: 1, SampleRate: 8000, BitsPerSample: 16}
	wr, err := NewWriter(ws, f)
	if err != nil {
		t.Fatal(err)
	}
	// 1 second of 8kHz 16-bit mono = 16000 bytes
	if _, err := wr.Write(make([]byte, 16000)); err != nil {
		t.Fatal(err)
	}
	if err := wr.Close(); err != nil {
		t.Fatal(err)
	}

	rd, err := NewReader(bytes.NewReader(ws.buf))
	if err != nil {
		t.Fatal(err)
	}
	if dur := rd.Duration(); dur != 1000 {
		t.Fatalf("initial duration = %d, want 1000", dur)
	}
	buf := make([]byte, 4000)
	if _, err := io.ReadFull(rd, buf); err != nil {
		t.Fatal(err)
	}
	if dur := rd.Duration(); dur != 1000 {
		t.Fatalf("duration after partial Read = %d, want 1000", dur)
	}
	if _, err := io.ReadAll(rd); err != nil {
		t.Fatal(err)
	}
	if dur := rd.Duration(); dur != 1000 {
		t.Fatalf("duration after ReadAll = %d, want 1000", dur)
	}
}
