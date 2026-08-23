package main

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"strconv"
	"time"

	"github.com/ntklink/gomediautils/go-mp4"
)

// mp4Epoch is the zero of every date in an mp4 file: midnight 1904-01-01,
// utc. Unix time counts from 1970, so the two differ by 66 years including
// 17 leap days.
var mp4Epoch = time.Date(1904, time.January, 1, 0, 0, 0, 0, time.UTC)

// SetMP4Time rewrites the creation and modification times of an mp4 in
// place, in the movie header, in every track header and in every media
// header.
//
// The file is patched rather than remuxed. Every one of these fields sits at
// a fixed offset inside its box and has the same width as what replaces it,
// so nothing moves and no other box has to be touched. That is what makes it
// safe to do to a file somebody else wrote.
func SetMP4Time(path string, when time.Time) error {
	seconds := int64(when.Sub(mp4Epoch) / time.Second)
	if seconds < 0 || seconds > math.MaxUint32 {
		return fmt.Errorf("%s is outside the range an mp4 32 bit date can hold", when)
	}

	f, err := os.OpenFile(path, os.O_RDWR, 0666)
	if err != nil {
		return err
	}
	defer f.Close()

	patched := 0
	err = walkBoxes(f, func(name string, payloadStart int64, size uint64) error {
		version, err := readVersion(f, payloadStart)
		if err != nil {
			return err
		}
		if err := writeTimes(f, payloadStart+4, version, uint64(seconds)); err != nil {
			return err
		}
		patched++
		return nil
	})
	if err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	if patched == 0 {
		return errors.New("no mvhd, tkhd or mdhd box found; is this really an mp4?")
	}
	return nil
}

// containerBoxes are the boxes we descend into rather than skip over. Every
// other box is stepped past whole.
var containerBoxes = map[string]bool{
	"moov": true,
	"trak": true,
	"mdia": true,
}

// timeBoxes hold a creation and a modification time right after the version
// and flags of their full box header.
var timeBoxes = map[string]bool{
	"mvhd": true,
	"tkhd": true,
	"mdhd": true,
}

// walkBoxes reads the box tree and calls onTimeBox for each header that
// carries dates.
func walkBoxes(f *os.File, onTimeBox func(name string, payloadStart int64, size uint64) error) error {
	for {
		start, err := f.Seek(0, io.SeekCurrent)
		if err != nil {
			return err
		}
		basebox := mp4.BasicBox{}
		if _, err := basebox.Decode(f); err != nil {
			return err
		}
		if basebox.Size < mp4.BasicBoxLen {
			return fmt.Errorf("box %q at offset %d claims to be %d bytes",
				string(basebox.Type[:]), start, basebox.Size)
		}
		name := string(basebox.Type[:])
		payloadStart, err := f.Seek(0, io.SeekCurrent)
		if err != nil {
			return err
		}

		switch {
		case timeBoxes[name]:
			if err := onTimeBox(name, payloadStart, basebox.Size); err != nil {
				return err
			}
			fallthrough
		case !containerBoxes[name]:
			// step over the payload; a container is left alone so the next
			// iteration reads its first child
			if _, err := f.Seek(start+int64(basebox.Size), io.SeekStart); err != nil {
				return err
			}
		}
	}
}

// readVersion reads the version byte of a full box without disturbing the
// file position callers rely on.
func readVersion(f *os.File, payloadStart int64) (uint8, error) {
	var b [1]byte
	if _, err := f.ReadAt(b[:], payloadStart); err != nil {
		return 0, err
	}
	return b[0], nil
}

// writeTimes replaces the creation and modification times that follow the
// version and flags. Version 1 stores them as 64 bit values, version 0 as 32.
func writeTimes(f *os.File, at int64, version uint8, seconds uint64) error {
	width := 4
	buf := make([]byte, 8)
	if version == 1 {
		width = 8
		binary.BigEndian.PutUint64(buf, seconds)
	} else {
		binary.BigEndian.PutUint32(buf, uint32(seconds))
	}
	for i := 0; i < 2; i++ { // creation time, then modification time
		if _, err := f.WriteAt(buf[:width], at+int64(i*width)); err != nil {
			return err
		}
	}
	return nil
}

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintf(os.Stderr, "usage: %s <file.mp4> <unix-seconds|RFC3339>\n", os.Args[0])
		os.Exit(2)
	}
	when, err := parseTime(os.Args[2])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	if err := SetMP4Time(os.Args[1], when); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Printf("%s now says it was created at %s\n", os.Args[1], when.UTC().Format(time.RFC3339))
}

func parseTime(s string) (time.Time, error) {
	if secs, err := strconv.ParseInt(s, 10, 64); err == nil {
		return time.Unix(secs, 0).UTC(), nil
	}
	return time.Parse(time.RFC3339, s)
}
