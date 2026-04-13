package main

import "strings"

// staleMarkers is the set of tracker messages that reliably indicate a
// torrent has been removed from the tracker (as opposed to transient
// "tracker is down" states). Each marker is matched case-insensitively as
// a substring.
var staleMarkers = []string{
	"not registered",
	"unregistered torrent",
	"torrent not found",
	"torrent does not exist",
	"torrent not exist",
	"torrent has been deleted",
	"torrent was deleted",
	"infohash not found",
}

// staleReason returns the tracker message that marked the torrent as stale,
// or an empty string if none of the trackers reported it as unregistered.
// Only rutracker torrents are considered — we can only resolve replacements
// via api.rutracker.cc.
func staleReason(trackers []Tracker) string {
	for _, tr := range trackers {
		if strings.HasPrefix(tr.URL, "**") {
			continue // qBit-internal DHT/PEX/LSD entries
		}
		if !isRutrackerURL(tr.URL) {
			continue
		}
		msg := strings.ToLower(strings.TrimSpace(tr.Msg))
		if msg == "" {
			continue
		}
		for _, m := range staleMarkers {
			if strings.Contains(msg, m) {
				return tr.Msg
			}
		}
	}
	return ""
}

func isRutrackerURL(u string) bool {
	lower := strings.ToLower(u)
	return strings.Contains(lower, "rutracker.org") ||
		strings.Contains(lower, "rutracker.net") ||
		strings.Contains(lower, "rutracker.nl") ||
		strings.Contains(lower, "rutracker.cc")
}
