package vision

import "testing"

func wall(x1, y1, x2, y2 float64) Segment {
	return Segment{A: Point{X: x1, Y: y1}, B: Point{X: x2, Y: y2}}
}

func TestSightIsBlockedByAWallBetween(t *testing.T) {
	blockers := []Segment{wall(5, 0, 5, 10)}

	if Visible(Point{X: 1, Y: 5}, Point{X: 9, Y: 5}, blockers) {
		t.Error("a wall directly between two points did not block sight")
	}
	if !Visible(Point{X: 1, Y: 5}, Point{X: 4, Y: 5}, blockers) {
		t.Error("a point on the same side was reported as hidden")
	}
	if !Visible(Point{X: 6, Y: 5}, Point{X: 9, Y: 5}, blockers) {
		t.Error("two points behind the wall cannot see each other")
	}
}

func TestSightPassesAroundAWallEnd(t *testing.T) {
	blockers := []Segment{wall(5, 0, 5, 4)}

	if !Visible(Point{X: 1, Y: 8}, Point{X: 9, Y: 8}, blockers) {
		t.Error("sight should pass below a wall that stops short")
	}
	if Visible(Point{X: 1, Y: 2}, Point{X: 9, Y: 2}, blockers) {
		t.Error("sight should be blocked where the wall stands")
	}
}

func TestParallelWallsDoNotBlock(t *testing.T) {
	blockers := []Segment{wall(0, 3, 10, 3)}

	if !Visible(Point{X: 1, Y: 1}, Point{X: 9, Y: 1}, blockers) {
		t.Error("a wall parallel to the line of sight blocked it")
	}
}

func TestTouchingEndpointsCount(t *testing.T) {
	blockers := []Segment{wall(5, 0, 5, 10)}

	if Visible(Point{X: 1, Y: 5}, Point{X: 5, Y: 5}, blockers) {
		t.Error("a target standing inside a wall should not be visible through it")
	}
}

func TestAnyViewpointIsEnough(t *testing.T) {
	blockers := []Segment{wall(5, 0, 5, 4)}
	target := Point{X: 9, Y: 2}

	blocked := []Point{{X: 1, Y: 2}}
	if VisibleFromAny(blocked, target, blockers) {
		t.Error("the only viewpoint is blocked, the target must be hidden")
	}

	withClearOne := []Point{{X: 1, Y: 2}, {X: 1, Y: 8}}
	if !VisibleFromAny(withClearOne, target, blockers) {
		t.Error("one clear viewpoint should be enough")
	}
}

func TestNoViewpointMeansNoRestriction(t *testing.T) {
	blockers := []Segment{wall(5, 0, 5, 10)}

	if !VisibleFromAny(nil, Point{X: 9, Y: 5}, blockers) {
		t.Error("a viewer with no token on the scene should not be blinded")
	}
}

func TestNoWallsMeansEverythingIsVisible(t *testing.T) {
	if !Visible(Point{X: 0, Y: 0}, Point{X: 100, Y: 100}, nil) {
		t.Error("an empty scene hid something")
	}
}
