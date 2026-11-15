# Load testing

`cmd/loadtest` is a separate workstation Go command, not part of the server binary. It joins through the HTTP API and public WSS, publishes real VP8 RTP and synthetic Opus, and counts packets received by all clients. It does not test browser decoding or perceived quality.

## Short isolated run

```bash
mkdir -p .dev/load
docker build -f deploy/load/Dockerfile -t checkpoint-load-video .
docker run --rm --cpus=1 --memory=512m --user "$(id -u):$(id -g)" \
  -v "$PWD/.dev/load:/out" checkpoint-load-video \
  -hide_banner -loglevel error -y -f lavfi -i testsrc2=size=640x360:rate=15 \
  -t 2 -c:v libvpx -b:v 250k -g 30 -deadline realtime -cpu-used 8 -f ivf /out/video.ivf
./scripts/build-native.sh
CHECKPOINT_LOAD=1 CHECKPOINT_LOAD_SECONDS=30 ./scripts/native-smoke.sh
```

The server and load-generator containers are each limited to 2 CPUs/2 GiB; the browser stage runs before load generation. Server and generator share one physical host, Docker networking, and a test CA. The generator trusts only the fixture CA; TLS verification is not disabled. TURN is disabled only in this load generator; the browser tests forced TURN separately.

Scenarios: Meeting with 20 publishing clients + one screen share; Webinar with 3 speakers + 100 viewers; 200 sequential joins. Camera: 640×360, VP8 at 15 fps, approximately 250 kbit/s, no simulcast. Audio: Opus silence, packets every 20 ms. The full participant set is held for the specified duration **after** clients join sequentially. The report is written to `.dev/load/report.json` without secrets.

Join p95 measures the time from HTTP join to successful SDK connection. First RTP is the first packet after join begins, not time to audible sound. Counters include warm-up/client joins and do not represent steady-state throughput. RTP is checked per client in aggregate, not by decoding every track. A successful short run does not establish an SLO or VPS capacity.

## Separate generator and extended acceptance tests

On a separate machine with Go and trusted public TLS, prepare a private access JSON containing `publicURL`, `meeting`, `host`, and `viewer`. These are invitations to dedicated test rooms: Meeting attendee, Webinar presenter, and Webinar attendee. Set sufficient use limits and revoke them after testing. Do not reuse rooms holding real meetings.

```bash
go run ./cmd/loadtest --access /private/load-access.json --video /private/video.ivf \
  --output /private/report.json --seconds 3600 --joins 200
```

For an extended run, use `--seconds 14400`. This sets the duration of **each** of the two scenarios: a full four-hour Meeting + Webinar run takes more than eight hours. Before running, record VPS CPU/RAM/NIC/region, versions, and the absence of competing load. Separately measure CPU/RAM/traffic, reconnects, and losses, and test browsers and real meetings. One-hour/four-hour acceptance testing is not complete; a short local test is not a representative pilot.
