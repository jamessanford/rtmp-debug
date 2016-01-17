**Show decoded RTMP commands from pcap wire data.**

Only a subset of RTMP is implemented!

If a stream contains both `connect` and `play` commands, an `rtmpdump` translation will be shown.

**Usage:**
```
rtmp-debug -i en4
rtmp-debug -f file.pcap
```

We use `tcpreader` to get the invidual TCP streams.

We create individual `processNewMessage` goroutines for every "chunk stream ID" inside the TCP stream.

Dechunked messages are sent into their "chunk stream ID" goroutine for decoding.

Decoded commands are sent into one `MessageFinalizer` per TCP stream.

`MessageFinalizer` sends anything useful into the final `results` channel.
