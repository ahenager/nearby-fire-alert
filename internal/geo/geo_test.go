package geo

import "testing"

func TestHaversineMiles(t *testing.T) {
	// Approx distance between OKC (35.0,-97.0) and Dallas (32.7767,-96.7970) ~ 154 mi.
	d := HaversineMiles(Point{35.0, -97.0}, Point{32.7767, -96.7970})
	if d < 145 || d > 165 {
		t.Fatalf("unexpected distance: %v", d)
	}
	// Same point -> 0.
	if HaversineMiles(Point{1, 2}, Point{1, 2}) != 0 {
		t.Fatal("same point should be 0")
	}
}

func TestInRadius(t *testing.T) {
	home := Point{35.0, -97.0}
	if !InRadius(home, Point{35.01, -97.0}, 5) {
		t.Fatal("should be in radius")
	}
	if InRadius(home, Point{35.2, -97.0}, 5) {
		t.Fatal("should be out of radius")
	}
}

func TestClassify(t *testing.T) {
	home := Point{35.0, -97.0}
	// Inside alert radius.
	if c := Classify(home, Point{35.005, -97.0}, 5, 25); c != ZoneAlert {
		t.Fatalf("expected alert, got %v", c)
	}
	// Inside watch (buffer) but outside alert.
	if c := Classify(home, Point{35.1, -97.0}, 5, 25); c != ZoneWatch {
		t.Fatalf("expected watch, got %v", c)
	}
	// Outside buffer.
	if c := Classify(home, Point{35.5, -97.0}, 5, 25); c != ZoneOutside {
		t.Fatalf("expected outside, got %v", c)
	}
	// Buffer clamped when smaller than alert.
	if c := Classify(home, Point{35.0, -97.0}, 10, 5); c != ZoneAlert {
		t.Fatalf("expected alert with clamped buffer, got %v", c)
	}
}

func TestPolygonContainsPoint(t *testing.T) {
	square := Polygon{
		{0, 0}, {0, 10}, {10, 10}, {10, 0},
	}
	if !square.ContainsPoint(Point{5, 5}) {
		t.Fatal("center should be inside")
	}
	if square.ContainsPoint(Point{15, 5}) {
		t.Fatal("point should be outside")
	}
}

func TestPolygonIntersectsCircle(t *testing.T) {
	square := Polygon{
		{0, 0}, {0, 10}, {10, 10}, {10, 0},
	}
	// Circle centered inside.
	if !square.IntersectsCircle(Point{5, 5}, 1) {
		t.Fatal("circle inside polygon should intersect")
	}
	// Circle near the x=10 edge (just outside), but not containing center or vertices.
	if !square.IntersectsCircle(Point{5, 10.005}, 1) {
		t.Fatal("circle near edge should intersect")
	}
	// Far away circle should not intersect.
	if square.IntersectsCircle(Point{50, 50}, 1) {
		t.Fatal("far circle should not intersect")
	}
}

func TestRepresentativePoint(t *testing.T) {
	pg := Polygon{{0, 0}, {0, 10}, {10, 10}, {10, 0}}
	rp := pg.RepresentativePoint()
	if rp.Latitude != 5 || rp.Longitude != 5 {
		t.Fatalf("unexpected centroid: %+v", rp)
	}
}
