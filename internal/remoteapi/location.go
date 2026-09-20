package remoteapi

import (
	"math"
	"strconv"
)

// Location is a geographic point. Text descriptions belong in InboundMessage.Text.
// Coordinates are carried as supplied by the backend, without projection changes.
type Location struct {
	Latitude  float64
	Longitude float64
}

// GeoURI returns an RFC 5870 coordinate URI, or an empty string for an absent or
// invalid point. A missing point must not silently become a location at (0, 0).
func (l *Location) GeoURI() string {
	if l == nil || math.IsNaN(l.Latitude) || math.IsNaN(l.Longitude) ||
		math.IsInf(l.Latitude, 0) || math.IsInf(l.Longitude, 0) ||
		l.Latitude < -90 || l.Latitude > 90 || l.Longitude < -180 || l.Longitude > 180 {
		return ""
	}
	return "geo:" + strconv.FormatFloat(l.Latitude, 'f', -1, 64) + "," + strconv.FormatFloat(l.Longitude, 'f', -1, 64)
}
