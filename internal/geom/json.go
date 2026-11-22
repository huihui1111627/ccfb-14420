package geom

import "encoding/json"

// MarshalJSON encodes a rect as [lo_0, ..., lo_{D-1}, hi_0, ..., hi_{D-1}].
func (r Rect) MarshalJSON() ([]byte, error) {
	out := make([]float64, 0, 2*r.D)
	out = append(out, r.min...)
	out = append(out, r.max...)
	return json.Marshal(out)
}

// UnmarshalJSON decodes the array form produced by MarshalJSON.
func (r *Rect) UnmarshalJSON(b []byte) error {
	var vals []float64
	if err := json.Unmarshal(b, &vals); err != nil {
		return err
	}
	if len(vals)%2 != 0 {
		return errInvalidRect
	}
	d := len(vals) / 2
	r.D = d
	r.min = append(r.min[:0], vals[:d]...)
	r.max = append(r.max[:0], vals[d:]...)
	return nil
}

type rectJSONError struct{}

func (rectJSONError) Error() string { return "geom: rect JSON must have 2*D values" }

var errInvalidRect = rectJSONError{}
