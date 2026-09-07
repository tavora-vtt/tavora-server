package vision

type Point struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
}

type Segment struct {
	A Point
	B Point
}

const epsilon = 1e-9

func orientation(a, b, c Point) float64 {
	return (b.X-a.X)*(c.Y-a.Y) - (b.Y-a.Y)*(c.X-a.X)
}

func onSegment(a, b, c Point) bool {
	return c.X <= max(a.X, b.X)+epsilon && c.X >= min(a.X, b.X)-epsilon &&
		c.Y <= max(a.Y, b.Y)+epsilon && c.Y >= min(a.Y, b.Y)-epsilon
}

func Crosses(first, second Segment) bool {
	d1 := orientation(first.A, first.B, second.A)
	d2 := orientation(first.A, first.B, second.B)
	d3 := orientation(second.A, second.B, first.A)
	d4 := orientation(second.A, second.B, first.B)

	if ((d1 > epsilon && d2 < -epsilon) || (d1 < -epsilon && d2 > epsilon)) &&
		((d3 > epsilon && d4 < -epsilon) || (d3 < -epsilon && d4 > epsilon)) {
		return true
	}

	switch {
	case abs(d1) <= epsilon && onSegment(first.A, first.B, second.A):
		return true
	case abs(d2) <= epsilon && onSegment(first.A, first.B, second.B):
		return true
	case abs(d3) <= epsilon && onSegment(second.A, second.B, first.A):
		return true
	case abs(d4) <= epsilon && onSegment(second.A, second.B, first.B):
		return true
	}
	return false
}

func Visible(from, to Point, blockers []Segment) bool {
	sight := Segment{A: from, B: to}
	for _, blocker := range blockers {
		if Crosses(sight, blocker) {
			return false
		}
	}
	return true
}

func VisibleFromAny(viewpoints []Point, to Point, blockers []Segment) bool {
	if len(viewpoints) == 0 {
		return true
	}
	for _, viewpoint := range viewpoints {
		if Visible(viewpoint, to, blockers) {
			return true
		}
	}
	return false
}

func abs(value float64) float64 {
	if value < 0 {
		return -value
	}
	return value
}

func min(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}

func max(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}
