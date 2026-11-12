package domain

// Chart is the calibration rectangle of one map, taken from MAP_CHANGE events:
// the x range is (X0, X1) and the y range is (Y0, Y1).
type Chart struct {
	MapID int     `json:"mapId"`
	Name  string  `json:"name"`
	X0    float64 `json:"x0"`
	X1    float64 `json:"x1"`
	Y0    float64 `json:"y0"`
	Y1    float64 `json:"y1"`
}

// Valid reports whether the chart has an id within bounds and finite corners within +/-CoordMax.
func (c Chart) Valid() bool {
	for _, v := range []float64{c.X0, c.X1, c.Y0, c.Y1} {
		if !finiteWithin(v, -CoordMax, CoordMax) {
			return false
		}
	}
	return c.MapID >= 0 && c.MapID <= MaxMapID
}
