# Codec Usage

Import the codec package as follows:

```go
import codec "github.com/ntklink/gomediautils/go-codec"
```

## Parse SPS, PPS, or VPS Data

The following H.264 example parses raw SPS data without an Annex B start code (`0x00000001`):

```go
rawSPS := []byte{0x67 /* remaining SPS bytes */}

bs := codec.NewBitStream(rawSPS)
sps := &codec.SPS{}
sps.Decode(bs)

if err := bs.Err(); err != nil {
	// Handle malformed or truncated SPS data.
}
```

## Get the Video Resolution

Pass an H.264 SPS NAL unit prefixed with an Annex B start code:

```go
rawSPS := []byte{0x00, 0x00, 0x00, 0x01, 0x67 /* remaining SPS bytes */}

width, height, err := codec.GetH264Resolution(rawSPS)
if err != nil {
	// Handle malformed or truncated SPS data.
}
```

## Create H.264 Extradata

`CreateH264AVCCExtradata` creates an AVC decoder configuration record from one or more SPS and PPS NAL units. Each NAL unit may include an Annex B start code.

```go
spss := [][]byte{
	{0x00, 0x00, 0x00, 0x01, 0x67 /* remaining SPS bytes */},
	{0x00, 0x00, 0x00, 0x01, 0x67 /* remaining SPS bytes */},
}

ppss := [][]byte{
	{0x00, 0x00, 0x00, 0x01, 0x68 /* remaining PPS bytes */},
	{0x00, 0x00, 0x00, 0x01, 0x68 /* remaining PPS bytes */},
}

extradata, err := codec.CreateH264AVCCExtradata(spss, ppss)
if err != nil {
	// Handle invalid parameter-set data.
}
```

## Convert H.264 Extradata to Annex B

Extradata read from formats such as FLV or MP4 can be split into SPS and PPS NAL units. Every returned NAL unit includes an Annex B start code.

```go
spss, ppss, err := codec.CovertExtradata(extradata)
if err != nil {
	// Handle malformed extradata.
}
```

## Create H.265 Extradata

Build an HEVC decoder configuration record by adding each VPS, SPS, and PPS to an `HEVCRecordConfiguration`:

```go
func createH265Extradata(vps, sps, pps []byte) ([]byte, error) {
	hvcc := codec.NewHEVCRecordConfiguration()

	if err := hvcc.UpdateVPS(vps); err != nil {
		return nil, err
	}
	if err := hvcc.UpdateSPS(sps); err != nil {
		return nil, err
	}
	if err := hvcc.UpdatePPS(pps); err != nil {
		return nil, err
	}

	return hvcc.Encode()
}
```

## Get Parameter-Set IDs

For H.264, use the method that matches whether the NAL unit includes an Annex B start code:

```go
// SPS with an Annex B start code.
spsID := codec.GetSPSIdWithStartCode(sps)

// SPS without an Annex B start code.
spsID = codec.GetSPSId(spsWithoutStartCode)

// PPS with an Annex B start code.
ppsID := codec.GetPPSIdWithStartCode(pps)

// PPS without an Annex B start code.
ppsID = codec.GetPPSId(ppsWithoutStartCode)
```
