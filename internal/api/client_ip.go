package api

import (
	"net"
	"net/http"
	"strings"
)

// clientIP is the address a request really came from, for rate limiting and
// for the source_ip of a webhook capture.
//
// The server is meant to sit behind one reverse proxy on the same machine.
// X-Forwarded-For is honoured ONLY when the direct peer is that proxy (a
// loopback address); from anyone else the header is ignored, since any
// client can send it. And only its LAST entry is used: that is the one the
// trusted proxy appended; everything to its left came from the client and can
// be forged.
func clientIP(r *http.Request) string {
	peer := r.RemoteAddr
	if host, _, err := net.SplitHostPort(peer); err == nil {
		peer = host
	}

	peerIP := net.ParseIP(peer)
	if peerIP == nil || !peerIP.IsLoopback() {
		return peer
	}

	forwarded := r.Header.Values("X-Forwarded-For")
	if len(forwarded) == 0 {
		return peer
	}

	// Several headers are equivalent to one comma-joined header.
	entries := strings.Split(forwarded[len(forwarded)-1], ",")
	last := strings.TrimSpace(entries[len(entries)-1])
	if ip := net.ParseIP(last); ip != nil {
		return ip.String()
	}

	return peer
}
