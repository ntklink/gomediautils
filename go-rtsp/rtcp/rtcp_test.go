package rtcp

import (
	"bytes"
	"sync"
	"testing"

	"github.com/ntklink/gomediautils/go-rtsp/rtp"
)

func TestCommDecodeShort(t *testing.T) {
	var c Comm
	if err := c.Decode([]byte{0x80, 200, 0, 6}); err == nil {
		t.Fatalf("length larger than data must error")
	}
	if err := c.Decode([]byte{0xa0, 200, 0, 0}); err == nil {
		t.Fatalf("padding on empty packet must error")
	}
	if err := c.Decode([]byte{0x80, 200, 0, 0}); err != nil {
		t.Fatalf("empty packet: %v", err)
	}
	// padding
	data := []byte{0xa0, 204, 0, 1, 1, 2, 0, 2}
	if err := c.Decode(data); err != nil || c.PayloadLen != 2 || len(c.PaddingData) != 2 {
		t.Fatalf("padding decode %v %+v", err, c)
	}
}

func TestSenderReportDecodeShort(t *testing.T) {
	sr := NewSenderReport()
	if err := sr.Decode([]byte{0x80, 200, 0, 1, 0, 0, 0, 1}); err == nil {
		t.Fatalf("short SR must error")
	}
	src := NewSenderReport()
	src.SSRC = 0x11223344
	src.NTP = 0x0102030405060708
	src.RC = 2
	src.Blocks = []ReportBlock{{SSRC: 1, Lost: 0x010203, ExtendHighestSeq: 5}, {SSRC: 2, Lost: 7}}
	enc := src.Encode()
	dec := NewSenderReport()
	if err := dec.Decode(enc); err != nil {
		t.Fatal(err)
	}
	if dec.SSRC != src.SSRC || dec.NTP != src.NTP || len(dec.Blocks) != 2 || dec.Blocks[1].SSRC != 2 || dec.Blocks[0].Lost != 0x010203 || dec.Blocks[1].Lost != 7 {
		t.Fatalf("SR round trip %+v", dec)
	}
	// RC claims more blocks than present
	enc[0] = 0x80 | 5
	if err := dec.Decode(enc); err == nil {
		t.Fatalf("RC beyond packet must error")
	}
}

func TestReceiverReportAndLostField(t *testing.T) {
	rr := NewReceiverReport()
	rr.SSRC = 9
	rr.RC = 1
	rr.Blocks = []ReportBlock{{SSRC: 3, Fraction: 10, Lost: 0x123456}}
	enc := rr.Encode()
	dec := NewReceiverReport()
	if err := dec.Decode(enc); err != nil {
		t.Fatal(err)
	}
	if dec.Blocks[0].Lost != 0x123456 || dec.Blocks[0].Fraction != 10 {
		t.Fatalf("RR block %+v", dec.Blocks[0])
	}
	if err := dec.Decode([]byte{0x81, 201, 0, 1, 0, 0, 0, 9}); err == nil {
		t.Fatalf("RR with missing block must error")
	}
}

func TestByeAndAppRoundTrip(t *testing.T) {
	bye := NewBye()
	bye.SC = 2
	bye.SSRCS = []uint32{1, 2}
	bye.Reason = "done"
	enc := bye.Encode()
	if enc[1] != RTCP_BYE || enc[0]&0x1f != 2 {
		t.Fatalf("bye header %x", enc[:4])
	}
	dec := NewBye()
	if err := dec.Decode(enc); err != nil {
		t.Fatal(err)
	}
	if len(dec.SSRCS) != 2 || dec.SSRCS[1] != 2 || dec.Reason != "done" {
		t.Fatalf("bye round trip %+v", dec)
	}
	if err := dec.Decode([]byte{0x82, 203, 0, 1, 0, 0, 0, 1}); err == nil {
		t.Fatalf("bye SC beyond packet must error")
	}

	app := NewApp()
	app.SubType = 3
	app.SSRC = 5
	app.Name = []byte("gome")
	app.AppData = []byte{1, 2, 3, 4, 5}
	enc = app.Encode()
	if enc[1] != RTCP_APP || enc[0]&0x1f != 3 {
		t.Fatalf("app header %x", enc[:4])
	}
	adec := NewApp()
	if err := adec.Decode(enc); err != nil {
		t.Fatal(err)
	}
	if adec.SSRC != 5 || !bytes.Equal(adec.Name, []byte("gome")) || !bytes.HasPrefix(adec.AppData, []byte{1, 2, 3, 4, 5}) {
		t.Fatalf("app round trip %+v", adec)
	}
	if err := adec.Decode([]byte{0x80, 204, 0, 1, 0, 0, 0, 5}); err == nil {
		t.Fatalf("app without name must error")
	}
}

func TestSdesRoundTrip(t *testing.T) {
	ctx := NewRtcpContext(77, 0, 90000)
	sdes := ctx.GenerateSDES(SDES_CNAME, "host")
	enc := sdes.Encode()
	dec := NewSourceDescription()
	if err := dec.Decode(enc); err != nil {
		t.Fatal(err)
	}
	if len(dec.Chunks) != 1 || dec.Chunks[0].SSRC != 77 || string(dec.Chunks[0].Item.Txt) != "host" {
		t.Fatalf("sdes round trip %+v", dec)
	}
}

func TestFractionLostClamped(t *testing.T) {
	ctx := NewRtcpContext(1, 0, 90000)
	// simulate receivedPrior > received interval (more received than expected)
	ctx.expectPrior = 100
	ctx.receivedPrior = 0
	ctx.received = 50
	ctx.baseSeq = 0
	ctx.maxSeq = 10
	rb := ctx.getReportBlock()
	if rb.Fraction != 0 {
		t.Fatalf("fraction should clamp to 0, got %d", rb.Fraction)
	}
}

// The rtp send path and the rtcp report timer are always different
// goroutines: an interval report is generated on a clock of its own while
// packets keep going out. Nothing a caller can do makes that not concurrent,
// so the context has to hold up under it on its own.
func TestRtcpContextIsConcurrencySafe(t *testing.T) {
	ctx := NewRtcpContext(0x11223344, 1000, 90000)

	var wg sync.WaitGroup
	stop := make(chan struct{})

	wg.Add(1)
	go func() {
		defer wg.Done()
		for seq := uint16(1000); ; seq++ {
			select {
			case <-stop:
				return
			default:
			}
			ctx.SendRtp(&rtp.RtpPacket{
				Header:  rtp.RtpHdr{SequenceNumber: seq, Timestamp: uint32(seq) * 3600},
				Payload: make([]byte, 100),
			})
		}
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		for seq := uint16(2000); ; seq++ {
			select {
			case <-stop:
				return
			default:
			}
			ctx.ReceivedRtp(&rtp.RtpPacket{
				Header:  rtp.RtpHdr{SequenceNumber: seq, Timestamp: uint32(seq) * 3600},
				Payload: make([]byte, 100),
			})
		}
	}()

	for i := 0; i < 2000; i++ {
		if sr := ctx.GenerateSR(); sr == nil {
			t.Fatal("GenerateSR returned nothing")
		}
		if rr := ctx.GenerateRR(); rr == nil {
			t.Fatal("GenerateRR returned nothing")
		}
		ctx.ReceivedSR(&SenderReport{SSRC: 0x55667788, NTP: uint64(i)})
	}
	close(stop)
	wg.Wait()
}
