package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/ntklink/gomediautils/go-mp4"
	"github.com/ntklink/gomediautils/go-mpeg2"
	"github.com/ntklink/gomediautils/go-srt"
)

// Serve accepts SRT publishers on l and records each stream into an mp4 in
// dir, named after the resource of its stream id: a caller publishing
// "#!::r=live/cam1,m=publish" ends up in live_cam1.mp4. Every recording
// runs in its own goroutine; done, when not nil, is told each file name
// once its recording is finished.
func Serve(l *srt.Listener, dir string, done chan<- string) error {
	for {
		conn, err := l.Accept()
		if err != nil {
			return err
		}
		go func() {
			name := recordingName(conn.StreamID())
			path := filepath.Join(dir, name+".mp4")
			if err := RecordTS(conn, path); err != nil {
				log.Printf("srt: %s: %v", name, err)
			}
			conn.Close()
			if done != nil {
				done <- path
			}
		}()
	}
}

// Authorize admits publishers only, the rule a recording server needs; a
// caller asking to play gets rejected before any state is set up for it.
func Authorize(req *srt.ConnRequest) error {
	info := srt.ParseStreamID(req.StreamID)
	if info.Resource == "" || (info.Mode != "publish" && !strings.HasPrefix(req.StreamID, "publish:")) {
		return &srt.RejectError{Reason: srt.RejectPeer}
	}
	return nil
}

func recordingName(streamID string) string {
	name := strings.TrimPrefix(srt.ParseStreamID(streamID).Resource, "publish:")
	return strings.NewReplacer("/", "_", "\\", "_", "..", "_").Replace(name)
}

// RecordTS demuxes the MPEG-TS arriving on r (an SRT connection reads as
// a byte stream) into an mp4 file.
func RecordTS(r io.Reader, mp4Path string) error {
	f, err := os.OpenFile(mp4Path, os.O_CREATE|os.O_TRUNC|os.O_RDWR, 0666)
	if err != nil {
		return err
	}
	defer f.Close()
	muxer, err := mp4.CreateMp4Muxer(f)
	if err != nil {
		return err
	}

	tracks := make(map[mpeg2.TS_STREAM_TYPE]uint32)
	var writeErr error
	demuxer := mpeg2.NewTSDemuxer()
	demuxer.OnFrame = func(cid mpeg2.TS_STREAM_TYPE, frame []byte, pts, dts uint64) {
		if writeErr != nil {
			return
		}
		tid, ok := tracks[cid]
		if !ok {
			switch cid {
			case mpeg2.TS_STREAM_H264:
				tid, writeErr = muxer.AddVideoTrack(mp4.MP4_CODEC_H264)
			case mpeg2.TS_STREAM_H265:
				tid, writeErr = muxer.AddVideoTrack(mp4.MP4_CODEC_H265)
			case mpeg2.TS_STREAM_AAC:
				tid, writeErr = muxer.AddAudioTrack(mp4.MP4_CODEC_AAC)
			case mpeg2.TS_STREAM_AUDIO_MPEG1, mpeg2.TS_STREAM_AUDIO_MPEG2:
				tid, writeErr = muxer.AddAudioTrack(mp4.MP4_CODEC_MP3)
			default:
				return
			}
			if writeErr != nil {
				return
			}
			tracks[cid] = tid
		}
		writeErr = muxer.Write(tid, frame, pts, dts)
	}
	// the publisher hanging up ends the stream: that is io.EOF from the
	// connection, and the recording is finished normally
	if err := demuxer.Input(r); err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	if writeErr != nil {
		return writeErr
	}
	if len(tracks) == 0 {
		return errors.New("no audio or video arrived")
	}
	return muxer.WriteTrailer()
}

func main() {
	addr := flag.String("listen", ":9000", "address to listen on")
	dir := flag.String("dir", ".", "where recordings go")
	passphrase := flag.String("passphrase", "", "encryption passphrase")
	flag.Parse()
	l, err := srt.Listen(*addr, srt.ListenConfig{
		Config:    srt.Config{Passphrase: *passphrase},
		Authorize: Authorize,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println("srt: listening on", l.Addr())
	if err := Serve(l, *dir, nil); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
