# GoMedia

A Go library for muxing and demuxing MPEG-TS, MPEG-PS, FLV, MP4, and RTMP media streams.

## Installation

```bash
go get github.com/ntklink/gomediautils
```

## Codec Utilities

See the [codec usage guide](go-codec/README.md) for more details.

- Decode SPS, PPS, VPS, and slice headers
- Decode and encode `HEVCDecoderConfigurationRecord`, `AVCDecoderConfigurationRecord`, AAC ADTS, and `AudioSpecificConfiguration`
- Decode Opus extra data (`OpusHead`) and packets (TOC), and encode Opus extra data
- Decode VP8 frame tags and keyframe headers
- Decode MP3 frame headers

## Supported Formats and Codecs

| Format | Mux | Demux |
| --- | --- | --- |
| MPEG-TS | H.264, H.265, AAC, MP3 | H.264, H.265, AAC, MP3 |
| MPEG-PS | H.264, H.265, AAC, G.711 A-law, G.711 μ-law | H.264, H.265, AAC, G.711 A-law, G.711 μ-law |
| FLV | H.264, H.265, AAC, G.711 A-law, G.711 μ-law, MP3 | H.264, H.265, AAC, G.711 A-law, G.711 μ-law, MP3 |
| MP4 | H.264, H.265, AAC, G.711 A-law, G.711 μ-law, MP3, Opus | H.264, H.265, AAC, G.711 A-law, G.711 μ-law, MP3 |
| fMP4 | H.264, H.265, AAC, G.711 A-law, G.711 μ-law | H.264, H.265, AAC, G.711 A-law, G.711 μ-law |
| Ogg | — | Opus, VP8 |

## RTMP

See the [RTMP usage guide](go-rtmp/README.md) for more details.

- Client and server support
- Play and publish support
- H.264, H.265, AAC, G.711 A-law, G.711 μ-law, and MP3 support

## RTSP

- Client and server support ([RFC 2326](https://datatracker.ietf.org/doc/html/rfc2326))
- Basic and Digest authentication
- RTP support ([RFC 3550](https://datatracker.ietf.org/doc/html/rfc3550))
- G.711, AAC, H.264, and H.265 support
  
