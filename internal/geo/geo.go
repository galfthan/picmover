// Package geo parses the coordinate formats found in media metadata.
package geo

import (
	"regexp"
	"strconv"
	"strings"
)

var iso6709Re = regexp.MustCompile(`^([+-]\d+(?:\.\d+)?)([+-]\d+(?:\.\d+)?)([+-]\d+(?:\.\d+)?)?(?:CRS[^/]*)?/?$`)

// ParseISO6709 parses strings such as "+60.1699+024.9384+010.000/" as written
// by QuickTime and Android into video files.
func ParseISO6709(s string) (lat, lon, alt float64, hasAlt, ok bool) {
	m := iso6709Re.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return 0, 0, 0, false, false
	}
	var err1, err2 error
	lat, err1 = strconv.ParseFloat(m[1], 64)
	lon, err2 = strconv.ParseFloat(m[2], 64)
	if err1 != nil || err2 != nil || !Valid(lat, lon) {
		return 0, 0, 0, false, false
	}
	if m[3] != "" {
		if a, err := strconv.ParseFloat(m[3], 64); err == nil {
			alt, hasAlt = a, true
		}
	}
	return lat, lon, alt, hasAlt, true
}

// ParseLatLon parses "lat,lon".
func ParseLatLon(s string) (lat, lon float64, ok bool) {
	parts := strings.Split(s, ",")
	if len(parts) != 2 {
		return 0, 0, false
	}
	var err1, err2 error
	lat, err1 = strconv.ParseFloat(strings.TrimSpace(parts[0]), 64)
	lon, err2 = strconv.ParseFloat(strings.TrimSpace(parts[1]), 64)
	if err1 != nil || err2 != nil || !Valid(lat, lon) {
		return 0, 0, false
	}
	return lat, lon, true
}

// Valid reports whether lat/lon are within range.
func Valid(lat, lon float64) bool {
	return lat >= -90 && lat <= 90 && lon >= -180 && lon <= 180
}
