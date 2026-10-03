// Package geo provides deterministic geospatial math for geofencing: haversine
// distance in miles, point-in-radius checks, buffer/watch-ring classification,
// and perimeter (polygon) intersection tests. No external dependencies.
package geo

import "math"

const earthRadiusMiles = 3958.7613

// Point is a latitude/longitude on the WGS84 ellipsoid approximated as a sphere.
type Point struct {
	Latitude  float64
	Longitude float64
}

// DegreesToRadians converts degrees to radians.
func DegreesToRadians(deg float64) float64 {
	return deg * math.Pi / 180.0
}

// RadiansToDegrees converts radians to degrees.
func RadiansToDegrees(rad float64) float64 {
	return rad * 180.0 / math.Pi
}

// HaversineMiles returns the great-circle distance in miles between two points
// on a sphere of radius earthRadiusMiles.
func HaversineMiles(a, b Point) float64 {
	lat1 := DegreesToRadians(a.Latitude)
	lon1 := DegreesToRadians(a.Longitude)
	lat2 := DegreesToRadians(b.Latitude)
	lon2 := DegreesToRadians(b.Longitude)

	dLat := lat2 - lat1
	dLon := lon2 - lon1

	h := math.Sin(dLat/2)*math.Sin(dLat/2) +
		math.Cos(lat1)*math.Cos(lat2)*math.Sin(dLon/2)*math.Sin(dLon/2)

	return 2 * earthRadiusMiles * math.Asin(math.Sqrt(h))
}

// InRadius reports whether b is within radiusMiles of a (inclusive).
func InRadius(a, b Point, radiusMiles float64) bool {
	return HaversineMiles(a, b) <= radiusMiles
}

// Zone classifies a point relative to a geofence's alert radius and buffer radius.
type Zone int

const (
	// ZoneOutside marks a point outside the buffer (watch) radius.
	ZoneOutside Zone = iota
	// ZoneWatch marks a point inside the buffer but outside the alert radius.
	ZoneWatch
	// ZoneAlert marks a point inside the alert radius.
	ZoneAlert
)

// Classify determines the zone for b relative to home.
//   - alertRadiusMiles: fires within this radius are alerted.
//   - bufferRadiusMiles: fires within this radius are watched/logged but not alerted.
//
// If bufferRadiusMiles < alertRadiusMiles, buffer is clamped to alertRadius.
func Classify(home, b Point, alertRadiusMiles, bufferRadiusMiles float64) Zone {
	if bufferRadiusMiles < alertRadiusMiles {
		bufferRadiusMiles = alertRadiusMiles
	}
	d := HaversineMiles(home, b)
	if d <= alertRadiusMiles {
		return ZoneAlert
	}
	if d <= bufferRadiusMiles {
		return ZoneWatch
	}
	return ZoneOutside
}

// Polygon is a ring of points defining a perimeter; the ring is treated as closed.
type Polygon []Point

// ContainsPoint reports whether p is inside the polygon (ray casting).
func (pg Polygon) ContainsPoint(p Point) bool {
	if len(pg) < 3 {
		return false
	}
	inside := false
	j := len(pg) - 1
	for i := 0; i < len(pg); i++ {
		a := pg[i]
		b := pg[j]
		if (a.Latitude > p.Latitude) != (b.Latitude > p.Latitude) &&
			p.Longitude < (b.Longitude-a.Longitude)*(p.Latitude-a.Latitude)/(b.Latitude-a.Latitude)+a.Longitude {
			inside = !inside
		}
		j = i
	}
	return inside
}

// distanceToSegment computes the great-circle distance in miles from p to the
// segment a-b using a planar equirectangular approximation over small spans,
// which is sufficient and deterministic for our use.
func distanceToSegment(p, a, b Point) float64 {
	const toRad = math.Pi / 180.0

	ax, ay := a.Longitude*toRad, a.Latitude*toRad
	bx, by := b.Longitude*toRad, b.Latitude*toRad
	px, py := p.Longitude*toRad, p.Latitude*toRad

	dx, dy := bx-ax, by-ay
	if dx == 0 && dy == 0 {
		return HaversineMiles(p, a)
	}
	t := ((px-ax)*dx + (py-ay)*dy) / (dx*dx + dy*dy)
	if t < 0 {
		t = 0
	} else if t > 1 {
		t = 1
	}
	projX, projY := ax+t*dx, ay+t*dy
	projDeg := Point{Latitude: projY / toRad, Longitude: projX / toRad}
	return HaversineMiles(p, projDeg)
}

// IntersectsCircle reports whether the polygon intersects a circle centered at
// center with the given radiusMiles. It considers: (1) the center inside the
// polygon, (2) any vertex within the radius, and (3) any edge passing within
// the radius.
func (pg Polygon) IntersectsCircle(center Point, radiusMiles float64) bool {
	if len(pg) < 3 {
		return false
	}
	if pg.ContainsPoint(center) {
		return true
	}
	for _, v := range pg {
		if HaversineMiles(center, v) <= radiusMiles {
			return true
		}
	}
	for i := 0; i < len(pg); i++ {
		a := pg[i]
		b := pg[(i+1)%len(pg)]
		if distanceToSegment(center, a, b) <= radiusMiles {
			return true
		}
	}
	return false
}

// RepresentativePoint returns the centroid of the ring (averaged). It is a
// deterministic fallback used when only a single point is needed.
func (pg Polygon) RepresentativePoint() Point {
	if len(pg) == 0 {
		return Point{}
	}
	var sumLat, sumLon float64
	for _, p := range pg {
		sumLat += p.Latitude
		sumLon += p.Longitude
	}
	n := float64(len(pg))
	return Point{Latitude: sumLat / n, Longitude: sumLon / n}
}
