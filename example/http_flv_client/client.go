package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/yapingcat/gomedia/go-codec"
	"github.com/yapingcat/gomedia/go-flv"
)

// Frame is one frame as it came off the wire, described but not copied.
type Frame struct {
	Codec codec.CodecID
	Size  int
	Pts   uint32
	Dts   uint32
}

// PullOptions bound a pull that would otherwise run until the server hangs
// up, which for a live stream is never.
type PullOptions struct {
	// MaxFrames stops after this many frames. Zero means no limit.
	MaxFrames int
	// Timeout stops after this long. Zero means no limit.
	Timeout time.Duration
}

// PullFLV downloads an http-flv stream to a file and reports the frames it
// carried.
//
// The bytes are written through untouched while a second copy goes to the flv
// reader. Saving the stream and understanding it are different jobs: a client
// that only remuxes what it parsed silently drops anything it did not
// understand, and one that only saves bytes cannot tell whether the stream is
// alive or has been sending nothing but empty tags for a minute.
func PullFLV(ctx context.Context, url, outPath string, opts PullOptions) ([]Frame, error) {
	if opts.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, opts.Timeout)
		defer cancel()
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: %s", url, resp.Status)
	}

	out, err := os.Create(outPath)
	if err != nil {
		return nil, err
	}
	defer out.Close()

	var frames []Frame
	reader := flv.CreateFlvReader()
	reader.OnFrame = func(cid codec.CodecID, frame []byte, pts, dts uint32) {
		frames = append(frames, Frame{Codec: cid, Size: len(frame), Pts: pts, Dts: dts})
	}

	buf := make([]byte, 32*1024)
	for {
		if opts.MaxFrames > 0 && len(frames) >= opts.MaxFrames {
			return frames, nil
		}
		n, err := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := out.Write(buf[:n]); werr != nil {
				return frames, werr
			}
			if perr := reader.Input(buf[:n]); perr != nil {
				return frames, perr
			}
		}
		if err != nil {
			// the server closing the connection, or the deadline running
			// out, is how a live pull normally ends
			if errors.Is(err, io.EOF) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return frames, nil
			}
			return frames, err
		}
	}
}

var (
	url     = flag.String("i", "", "http-flv url to pull")
	outFile = flag.String("o", "", "flv file to write")
	seconds = flag.Int("t", 0, "stop after this many seconds, 0 for no limit")
)

func main() {
	flag.Parse()
	if *url == "" || *outFile == "" {
		flag.Usage()
		os.Exit(2)
	}
	frames, err := PullFLV(context.Background(), *url, *outFile, PullOptions{
		Timeout: time.Duration(*seconds) * time.Second,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	for _, f := range frames {
		fmt.Printf("%s pts %d dts %d, %d bytes\n", codec.CodecString(f.Codec), f.Pts, f.Dts, f.Size)
	}
	fmt.Printf("%d frames written to %s\n", len(frames), *outFile)
}
