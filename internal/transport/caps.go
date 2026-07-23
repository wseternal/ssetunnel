package transport

import (
	"fmt"
	"strconv"
	"strings"
)

// Capability negotiation wire contract (cycle-2 plan decision 3):
//
//  1. The server advertises its capability constants on the /events 200
//     response: X-SSET-Caps: concurrency=4;batch=1048576;gzip.
//  2. The agent cannot know the advertisement when it connects, so it
//     sends its WANTED caps (from flags) on the /events request. The
//     effective set is the per-axis minimum of wanted and advertised;
//     both sides compute it independently (NegotiateCaps).
//  3. The server arms the reorder window — and with it the whole
//     cycle-2 upstream protocol, including gzip acceptance — iff the
//     request's concurrency > 1. Because the advertised concurrency is a
//     constant > 1, "request concurrency > 1" holds exactly when the
//     agent's negotiated concurrency > 1: a window exists iff BOTH sides
//     actually run concurrent upstream POSTs.
//  4. X-SSET-Flags: gzip is therefore valid only on windowed sessions;
//     the server rejects it with 400 otherwise (fail closed), so the
//     agent enables gzip only when it negotiated concurrency > 1 itself.
//  5. Absent or malformed caps on either side mean cycle-1 behavior for
//     that axis — negotiation never produces an error.
type Caps struct {
	Concurrency int  // upstream POST sender depth; 0 or 1 = serial cycle-1
	Batch       int  // max batch bytes; 0 = unspecified
	Gzip        bool // gzip-per-batch allowed
}

// ParseCaps parses an X-SSET-Caps header value
// ("concurrency=4;batch=1048576;gzip"). It fails closed per axis: absent,
// malformed, or negative values yield the zero (cycle-1) value for that
// axis; unknown fields are ignored. It never errors.
func ParseCaps(h string) Caps {
	var c Caps
	for field := range strings.SplitSeq(h, ";") {
		k, v, _ := strings.Cut(strings.TrimSpace(field), "=")
		switch strings.TrimSpace(k) {
		case "concurrency":
			if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && n >= 0 {
				c.Concurrency = n
			}
		case "batch":
			if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && n >= 0 {
				c.Batch = n
			}
		case "gzip":
			c.Gzip = true
		}
	}
	return c
}

// NegotiateCaps intersects wanted and advertised caps: per-axis minimum,
// with gzip requiring both sides (decision 3: fail closed per axis). A
// zero advertised axis means the server did not speak that capability at
// all — it fails closed to the cycle-1 profile for that axis (serial
// sender, DefaultMaxBatchSize), never to the agent's configured value.
func NegotiateCaps(want, advertised Caps) Caps {
	if advertised.Concurrency == 0 {
		advertised.Concurrency = 1
	}
	if advertised.Batch == 0 {
		advertised.Batch = DefaultMaxBatchSize
	}
	return Caps{
		Concurrency: min(want.Concurrency, advertised.Concurrency),
		Batch:       min(want.Batch, advertised.Batch),
		Gzip:        want.Gzip && advertised.Gzip,
	}
}

// String serializes caps for the wire: "concurrency=4;batch=65536;gzip".
// Zero-valued axes are omitted except concurrency, which is always sent.
func (c Caps) String() string {
	s := fmt.Sprintf("concurrency=%d", c.Concurrency)
	if c.Batch > 0 {
		s += fmt.Sprintf(";batch=%d", c.Batch)
	}
	if c.Gzip {
		s += ";gzip"
	}
	return s
}
