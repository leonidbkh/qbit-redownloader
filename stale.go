package main

import "strings"

// obsoleteStatuses are tor_status values that mean the torrent is no longer
// the current version on rutracker (topic exists, but is dead in some way).
// Source: rutracker.wiki FAQ on torrent statuses.
//
//	 7  поглощено (absorbed by another release)
//	 8  повтор / дубликат
//	10  закрыто правообладателем
//	11  закрыто
var obsoleteStatuses = map[int]string{
	7:  "поглощено",
	8:  "дубликат",
	10: "закрыто правообладателем",
	11: "закрыто",
}

func isObsoleteStatus(s int) bool {
	_, ok := obsoleteStatuses[s]
	return ok
}

// staleMarkers is the set of tracker messages that indicate a torrent has
// been removed from the tracker. Used only as a fallback when the rutracker
// API is unreachable — the API-based detector is much more reliable.
var staleMarkers = []string{
	"torrent not registered",
	"unregistered torrent",
	"torrent not found",
	"torrent does not exist",
	"torrent has been deleted",
	"torrent was deleted",
	"infohash not found",
	"торрент не зарегистрирован",
	"не зарегистрирован",
	"торрент не найден",
	"торрент удалён",
	"торрент удален",
	"раздача удалена",
}

// staleReasonFromTracker scans tracker messages for known "torrent removed"
// signatures. Returns the matching message, or "" if none fired.
func staleReasonFromTracker(trackers []Tracker) string {
	for _, tr := range trackers {
		if strings.HasPrefix(tr.URL, "**") {
			continue // qBit-internal DHT/PEX/LSD entries
		}
		if !isRutrackerAnnouncer(tr.URL) {
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

// isRutrackerAnnouncer reports whether the tracker announce URL belongs to
// rutracker. The real BT announcer is bt*.t-ru.org — matching rutracker.*
// alone misses every real torrent.
func isRutrackerAnnouncer(u string) bool {
	lower := strings.ToLower(u)
	return strings.Contains(lower, "rutracker.org") ||
		strings.Contains(lower, "rutracker.net") ||
		strings.Contains(lower, "rutracker.nl") ||
		strings.Contains(lower, "rutracker.cc") ||
		strings.Contains(lower, "t-ru.org")
}
