package rtree

import "ancienttrees/internal/geom"

// RecordLabel is the leaf payload projection used by the visualisation page.
type RecordLabel struct {
	ID      int     `json:"id"`
	Kind    string  `json:"kind"`
	Sampled bool    `json:"sampled"`
	Age     float64 `json:"age"`
}

// NodeView is a JSON-friendly snapshot of one node.
type NodeView struct {
	ID      int         `json:"id"`
	Level   int         `json:"level"`
	Depth   int         `json:"depth"`
	Leaf    bool        `json:"leaf"`
	Bounds  geom.Rect   `json:"bounds"`
	Parent  int         `json:"parent,omitempty"`
	Entries []EntryView `json:"entries"`
}

// EntryView is one entry inside a NodeView.
type EntryView struct {
	Bounds geom.Rect `json:"bounds"`
	// Child is the target node for internal entries.
	Child int `json:"child,omitempty"`
	// Records lists the record ids for leaf entries.
	Records []int `json:"records,omitempty"`
}

// SnapshotDTO is the full tree snapshot for the page.
type SnapshotDTO struct {
	Dim     int           `json:"dim"`
	Height  int           `json:"height"`
	Records int           `json:"records"`
	Labels  []RecordLabel `json:"labels"`
	Nodes   []NodeView    `json:"nodes"`
	Stats   StatsSnapshot `json:"stats"`
}

// Snapshot walks the tree once and returns every node in breadth-first order,
// with leaf entries reduced to the record ids they hold. labels maps a leaf
// payload to its record id; it may be nil (ids are then omitted).
func (t *Tree) Snapshot(labels func(data any) (id int, label RecordLabel)) SnapshotDTO {
	dto := SnapshotDTO{Dim: t.cfg.Dim, Height: t.Height(), Records: t.recordCount, Stats: t.Stats.Snapshot()}
	if t.Root == nil {
		dto.Nodes = []NodeView{}
		dto.Labels = []RecordLabel{}
		return dto
	}
	queue := []*Node{t.Root}
	depths := map[int]int{t.Root.ID: 0}
	for len(queue) > 0 {
		n := queue[0]
		queue = queue[1:]
		v := NodeView{ID: n.ID, Level: n.Level, Depth: depths[n.ID], Leaf: n.Leaf, Bounds: n.Bounds}
		if n.Parent != nil {
			v.Parent = n.Parent.ID
		}
		v.Entries = make([]EntryView, 0, len(n.Entries))
		for _, e := range n.Entries {
			ev := EntryView{Bounds: e.Bounds()}
			if e.child != nil {
				ev.Child = e.child.ID
				depths[e.child.ID] = depths[n.ID] + 1
				queue = append(queue, e.child)
			} else if labels != nil {
				id, lab := labels(e.Data)
				lab.ID = id
				ev.Records = []int{id}
				if id < 0 || id >= 1<<22 {
					panic("rtree: record id out of range")
				}
				if id >= len(dto.Labels) {
					grown := make([]RecordLabel, id+1)
					copy(grown, dto.Labels)
					dto.Labels = grown
				}
				dto.Labels[id] = lab
			}
			v.Entries = append(v.Entries, ev)
		}
		dto.Nodes = append(dto.Nodes, v)
	}
	if dto.Labels == nil {
		dto.Labels = []RecordLabel{}
	}
	return dto
}
