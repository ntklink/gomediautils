package main

import (
	"fmt"
	"os"

	"github.com/yapingcat/gomedia/go-codec"
)

// MP3Frame is one frame of an mp3 elementary stream, described by its header.
type MP3Frame struct {
	Offset       int
	Size         int
	BitRate      int
	SampleRate   int
	ChannelCount int
	SampleSize   int
}

// Duration is how long the frame plays, in milliseconds. It is not the same
// for every frame in a stream: layer 3 mpeg-1 frames hold 1152 samples,
// mpeg-2 and 2.5 hold 576, and layer 1 holds 384.
func (f MP3Frame) Duration() float64 {
	if f.SampleRate == 0 {
		return 0
	}
	return float64(f.SampleSize) * 1000 / float64(f.SampleRate)
}

// ReadMP3Frames walks an mp3 file and describes every frame in it.
//
// The frames are found by walking headers rather than by scanning for the
// syncword, so id3 tags are skipped rather than mistaken for audio, and a
// bitrate that changes from one frame to the next is followed rather than
// assumed away.
func ReadMP3Frames(path string) ([]MP3Frame, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var frames []MP3Frame
	offset := 0
	err = codec.SplitMp3Frames(data, func(head *codec.MP3FrameHead, frame []byte) {
		// SplitMp3Frames hands over sub slices of data, so the distance
		// between the two backing arrays is the offset in the file
		frames = append(frames, MP3Frame{
			Offset:       offset,
			Size:         len(frame),
			BitRate:      head.GetBitRate(),
			SampleRate:   head.GetSampleRate(),
			ChannelCount: head.GetChannelCount(),
			SampleSize:   head.SampleSize,
		})
		offset += len(frame)
	})
	if err != nil {
		return frames, err
	}
	return frames, nil
}

// TotalDuration is how long the whole stream plays, in milliseconds.
func TotalDuration(frames []MP3Frame) float64 {
	total := 0.0
	for _, f := range frames {
		total += f.Duration()
	}
	return total
}

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintf(os.Stderr, "usage: %s <input.mp3>\n", os.Args[0])
		os.Exit(2)
	}
	frames, err := ReadMP3Frames(os.Args[1])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	for i, f := range frames {
		fmt.Printf("frame %d: offset %d size %d, %d bps, %d Hz, %d channels, %.2f ms\n",
			i, f.Offset, f.Size, f.BitRate, f.SampleRate, f.ChannelCount, f.Duration())
	}
	fmt.Printf("%d frames, %.3f seconds\n", len(frames), TotalDuration(frames)/1000)
}
