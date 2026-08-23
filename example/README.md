# examples

Each directory is a small program showing one thing the library does. Every
one of them is covered by a test that runs it against real media.

## Running the tests

The tests generate their input with ffmpeg and check the output with ffprobe,
so they need both on the machine:

```sh
go test ./example/...
```

Without ffmpeg every one of them skips, and the rest of the repository still
builds and tests as usual. To point at a specific build:

```sh
GOMEDIAUTILS_FFMPEG=/opt/ffmpeg/bin/ffmpeg GOMEDIAUTILS_FFPROBE=/opt/ffmpeg/bin/ffprobe go test ./example/...
```

The client tests additionally want a streaming server to talk to. Put
[mediamtx](https://github.com/bluenviron/mediamtx) on PATH (or name it with
`GOMEDIAUTILS_MEDIAMTX`) and they start a private one on ephemeral ports:

```sh
GOMEDIAUTILS_MEDIAMTX=/opt/mediamtx/mediamtx go test ./example/...
```

An already running server elsewhere works too, though its link is out of the
test's control so the assertions there only check that media really flows:

```sh
GOMEDIAUTILS_REMOTE=streaming.example.com go test ./example/...          # default ports
GOMEDIAUTILS_REMOTE=streaming.example.com:1935:8554:8888 go test ./...   # rtmp:rtsp:hls
```

Source clips are cached under `$TMPDIR/gomediautils-mediatest-cache`, so a second
run does no encoding. Delete that directory to force a regenerate.

## What the tests check

Muxing and demuxing against the library's own counterpart proves very little:
both sides can agree on the same mistake. These tests put a real
implementation on the other end instead.

- **ffmpeg produces the input.** A clip encoded by ffmpeg has b frames, a
  reordered presentation, parameter sets in the places a real encoder puts
  them, and timestamps that do not start at zero.
- **ffmpeg reads the output.** `ffprobe` has to recognise the container and
  find the streams, and `ffmpeg -xerror -err_detect explode` has to decode the
  whole file without a single warning.
- **The pictures have to survive.** The decisive check is a checksum of the
  decoded frames: the source and whatever GoMediaUtils wrote are both decoded
  to raw yuv and the hashes compared. A remux that drops a frame, loses the
  parameter sets or mangles a composition offset fails there even when every
  count still matches.
- **Timestamps have to make sense.** Decode timestamps must not go backwards,
  which is what a muxer that mishandles reordering produces.

The network examples go further and put a real peer on the wire, in both
directions:

- **GoMediaUtils' servers, ffmpeg's clients.** ffmpeg publishes into the rtmp and
  rtsp servers and plays back out of them, over the same handshake, chunking,
  sdp and rtp framing any other client would use.
- **GoMediaUtils' clients, somebody else's server.** The rtmp and rtsp clients
  publish into and play out of mediamtx, whose rtsp side is gortsplib. Neither
  shares any code or assumption with this library, so a shortcut that two
  GoMediaUtils components happen to agree on fails there.

A few tests measure against what actually arrived rather than against the
file that was sent. `demux_ts_over_rtp` records the bytes off the socket and
demuxes that recording with ffmpeg, because ffmpeg's rtp sender only emits
full datagrams and quietly drops the last partial one; holding GoMediaUtils to
the source file would be measuring that instead.

## Covered examples

### containers, file to file

| example | what the test drives |
| --- | --- |
| `convert_ts_to_mp4` | ts demuxer + mp4 muxer, h264/h265/aac/mp3, b frames, long gop |
| `covert_flv_to_mp4` | flv demuxer + mp4 muxer |
| `convert_mp4_to_flv` | mp4 demuxer + flv muxer, including hevc over enhanced flv |
| `convert_flv_to_fmp4` | fragmented mp4: moof/traf/trun/mfra |
| `convert_flv_to_hls_fmp4` | HLS init and media segments, played through the playlist |
| `hls_fmp4_h265` | the same for hevc, where the init segment carries an hvcC |
| `play_mp4_with_hls` | mp4 cut into ts segments on demand, every one starting on a keyframe |
| `edit_mp4_time` | patching mvhd/tkhd/mdhd dates in place without moving a byte |

### demuxers

| example | what the test drives |
| --- | --- |
| `demux_ts` | ts to elementary streams, checked against ffmpeg's own extraction |
| `demux_ps` | program stream to elementary streams |
| `demux_flv` | flv to elementary streams, including the sequence headers |
| `demux_mp4` | mp4 and fragmented mp4, with b frames so ctts is exercised |
| `demux_fmp4` | an hls init segment plus media segments, the way a player gets them |
| `demux_mp4_memeory_io` | the same out of memory, plus the ReadSeeker against bytes.Reader |
| `demux_ogg` | ogg demuxer, opus carried into mp4 |
| `read_mp3` | frame walk compared to ffprobe frame by frame, offsets and sizes |

### muxers

| example | what the test drives |
| --- | --- |
| `mux_ts` | ts muxer fed a bare Annex-B stream |
| `mux_ts_aac` | audio only ts, where the pcr has to ride on the audio pid |
| `mux_ts_mp3` | mp3 in ts, constant and variable bitrate |
| `mux_ps` | program stream muxer, the gb28181 container |
| `mux_ps_h264` | program stream from a bare elementary stream, h264 and h265 |
| `mux_flv` | flv round trip through elementary streams |
| `mux_mp4_memory_io` | mp4 built in memory, plus the WriteSeeker against a real file |

### network

| example | what the test drives |
| --- | --- |
| `rtmp_server` | ffmpeg publishes and plays over rtmp |
| `rtsp_server` | ffmpeg announces, pushes and plays over rtsp, with digest auth |
| `rtsp_play_server` | ffmpeg plays mpeg-ts over rtp from GoMediaUtils, over tcp and udp |
| `http_flv_server` | http-flv, remuxed per frame, plus a path traversal check |
| `rtmp_publish_client` | GoMediaUtils publishes into a third party server |
| `rtmp_play_client` | GoMediaUtils plays from a third party server |
| `mp4_to_rtmp_server` | GoMediaUtils publishes an mp4, interleaved and with b frame timing |
| `rtsp_play_client_rtp_over_rtsp` | GoMediaUtils plays rtsp with interleaved rtp |
| `rtsp_client_rtp_over_udp` | the same over its own udp port pair |
| `rtsp_push_client_rtp_over_rtsp` | GoMediaUtils announces and records into gortsplib |
| `http_flv_client` | GoMediaUtils pulls http-flv and parses it as it arrives |
| `demux_ts_over_rtp` | ffmpeg sends mpeg-ts over rtp, GoMediaUtils reassembles it |

## Helper package

`internal/mediatest` holds the shared pieces: locating ffmpeg, generating
clips, parsing ffprobe output, comparing decoded streams and running a
background publisher.
