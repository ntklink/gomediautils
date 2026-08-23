# RTMP Usage

The RTMP package handles the RTMP protocol layer only. The caller is responsible for managing the network connection—typically TCP—and sending and receiving data.

Data flows through the package in two directions:

- Pass bytes received from the network to `Input`.
- Send bytes emitted by the `SetOutput` callback to the remote peer.

## Pull Client Example

The following example connects to an RTMP server and starts a pull client. Both `OnFrame` and `SetOutput` are required for pull clients.

```go
func pullRTMP(host, rtmpURL string) error {
	// Connect to the remote RTMP server.
	conn, err := net.Dial("tcp4", host)
	if err != nil {
		return err
	}
	defer conn.Close()

	// Create a client with a custom chunk size and complex handshake.
	client := rtmp.NewRtmpClient(
		rtmp.WithChunkSize(6000),
		rtmp.WithComplexHandshake(),
	)

	// Handle decoded audio and video frames.
	client.OnFrame(func(cid codec.CodecID, pts, dts uint32, frame []byte) {
		// Process the received frame here.
	})

	// Forward protocol output to the remote peer.
	client.SetOutput(func(data []byte) error {
		_, err := conn.Write(data)
		return err
	})

	// Start the RTMP handshake and pull workflow.
	if err := client.Start(rtmpURL); err != nil {
		return err
	}

	// Feed incoming network data to the RTMP client.
	buf := make([]byte, 32*1024)
	for {
		n, err := conn.Read(buf)
		if err != nil {
			return err
		}
		if err := client.Input(buf[:n]); err != nil {
			return err
		}
	}
}
```
