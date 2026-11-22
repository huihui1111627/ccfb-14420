// Package app holds the ancient-tree domain model, synthetic city generation,
// the spatial index store and brute-force reference queries.
package app

import (
	"math"
	"math/rand"
	"sort"

	"ancienttrees/internal/geom"
)

// DateOrigin is the survey-date origin; dates are stored as months since it.
const DateOrigin = "2020-01"

// Tree is one registered ancient tree.
type Tree struct {
	ID      int
	Species string
	X, Y    float64
	Date    float64 // months since DateOrigin
	Crown   [][2]float64
	BBox2   geom.Box
	BBox3   geom.Box
}

// Rect returns the indexed box in the requested dimensionality.
func (t *Tree) Rect(dims uint8) geom.Box {
	if dims == 3 {
		return t.BBox3
	}
	return t.BBox2
}

// RedLine is one construction red line (an axis-aligned 2-D rectangle).
type RedLine struct {
	ID    int
	X0    float64
	Y0    float64
	X1    float64
	Y1    float64
	Label string
}

// Rect returns the red line as a box.
func (l *RedLine) Rect() geom.Box {
	return geom.NewBox(2, [3]float64{l.X0, l.Y0, 0}, [3]float64{l.X1, l.Y1, 0})
}

// Poly returns the red line corners (for display; exact tests use Rect).
func (l *RedLine) Poly() [][2]float64 {
	return [][2]float64{{l.X0, l.Y0}, {l.X1, l.Y0}, {l.X1, l.Y1}, {l.X0, l.Y1}}
}

// Cluster is a city district used to make tree positions clumpy.
type Cluster struct {
	X, Y  float64
	Sigma float64
}

// flatRec is the cache-friendly copy of a tree's boxes used by the
// brute-force reference implementation.
type flatRec struct {
	id   int32
	lo   [3]float64
	hi   [3]float64
	date float64
}

// Dataset is the mutable set of trees plus red lines.
type Dataset struct {
	Trees map[int]*Tree
	live  []*Tree // compact, unordered; brute force scans this
	pos   map[int]int
	flat  []flatRec
	fpos  map[int]int

	Lines    []*RedLine
	Clusters []Cluster

	Bounds  geom.Box // 2-D city bounds
	nextID  int
	species []string
}

// Bounds2 builds the city bounds box.
func cityBounds() geom.Box {
	return geom.NewBox(2, [3]float64{0, 0, 0}, [3]float64{20000, 20000, 0})
}

// NewDataset generates n trees clumped into city districts, plus red lines.
func NewDataset(seed int64, n, nLines int) *Dataset {
	rng := rand.New(rand.NewSource(seed))
	d := &Dataset{
		Trees:  make(map[int]*Tree, n),
		pos:    make(map[int]int, n),
		flat:   make([]flatRec, 0, n),
		fpos:   make(map[int]int, n),
		Bounds: cityBounds(),
		species: []string{
			"银杏", "国槐", "香樟", "侧柏", "枫香", "榆树",
			"朴树", "皂荚", "重阳木", "榉树", "罗汉松", "榕树",
		},
	}
	rngShuffle(rng, d.species)

	// A dense old town plus outlying districts.
	d.Clusters = []Cluster{
		{5200, 5600, 620},
		{12600, 8800, 900},
		{8600, 14400, 780},
		{15600, 14900, 560},
		{3600, 13200, 700},
		{17500, 4300, 460},
		{10800, 3200, 640},
		{14400, 10400, 380},
	}
	weights := []float64{26, 20, 14, 9, 8, 6, 7, 10}
	totalW := 0.0
	for _, w := range weights {
		totalW += w
	}

	for i := 0; i < n; i++ {
		var x, y float64
		r := rng.Float64() * totalW
		if rng.Float64() < 0.035 {
			// Roadside stragglers: scattered through the whole city.
			x = rng.Float64() * 19800
			y = rng.Float64() * 19800
		} else {
			acc := 0.0
			ci := 0
			for j, w := range weights {
				acc += w
				if r < acc {
					ci = j
					break
				}
			}
			c := d.Clusters[ci]
			x = c.X + rng.NormFloat64()*c.Sigma
			y = c.Y + rng.NormFloat64()*c.Sigma
			x = math.Max(5, math.Min(19995, x))
			y = math.Max(5, math.Min(19995, y))
		}
		t := &Tree{
			ID:      d.nextID,
			Species: d.species[rng.Intn(len(d.species))],
			X:       round2(x),
			Y:       round2(y),
			Date:    round2(rng.Float64() * 120),
			Crown:   generateCrown(rng, x, y),
		}
		d.nextID++
		d.finishTree(t)
		d.add(t)
	}
	for i := 0; i < nLines; i++ {
		d.Lines = append(d.Lines, generateLine(rng, d, d.nextID))
		d.nextID++
	}
	return d
}

func round2(v float64) float64 { return math.Round(v*100) / 100 }

// generateCrown makes an irregular, star-shaped canopy polygon around (x,y).
func generateCrown(rng *rand.Rand, x, y float64) [][2]float64 {
	n := 6 + rng.Intn(7) // 6..12 vertices
	base := 2.5 + rng.Float64()*9.5
	rot := rng.Float64() * 2 * math.Pi
	poly := make([][2]float64, n)
	for i := 0; i < n; i++ {
		a := rot + 2*math.Pi*float64(i)/float64(n) + (rng.Float64()-0.5)*0.35
		r := base * (0.55 + rng.Float64()*0.85)
		poly[i] = [2]float64{
			math.Round((x+r*math.Cos(a))*100) / 100,
			math.Round((y+r*math.Sin(a))*100) / 100,
		}
	}
	return poly
}

// RefreshBBoxes recomputes a tree's derived boxes after its coordinates or
// date changed.
func (d *Dataset) RefreshBBoxes(t *Tree) {
	d.finishTree(t)
	if i, ok := d.fpos[t.ID]; ok {
		d.flat[i] = flatRec{id: int32(t.ID), lo: t.BBox2.Lo, hi: t.BBox2.Hi, date: t.Date}
	}
}

func (d *Dataset) finishTree(t *Tree) {
	t.BBox2 = geom.PolyBBox(t.Crown)
	t.BBox3 = geom.NewBox(3,
		[3]float64{t.BBox2.Lo[0], t.BBox2.Lo[1], t.Date},
		[3]float64{t.BBox2.Hi[0], t.BBox2.Hi[1], t.Date})
}

func (d *Dataset) add(t *Tree) {
	d.Trees[t.ID] = t
	d.pos[t.ID] = len(d.live)
	d.live = append(d.live, t)
	d.fpos[t.ID] = len(d.flat)
	d.flat = append(d.flat, flatRec{
		id:   int32(t.ID),
		lo:   t.BBox2.Lo,
		hi:   t.BBox2.Hi,
		date: t.Date,
	})
}

// NextID hands out an id for a newly inserted tree.
func (d *Dataset) NextID() int {
	id := d.nextID
	d.nextID++
	return id
}

// NewTree creates a tree at an arbitrary position (used by mutations).
func (d *Dataset) NewTree(rng *rand.Rand, x, y float64) *Tree {
	t := &Tree{
		ID:      d.NextID(),
		Species: d.species[rng.Intn(len(d.species))],
		X:       round2(x),
		Y:       round2(y),
		Date:    round2(rng.Float64() * 120),
		Crown:   generateCrown(rng, x, y),
	}
	d.finishTree(t)
	return t
}

// Add registers a tree.
func (d *Dataset) Add(t *Tree) { d.add(t) }

// Remove unregisters a tree and returns it.
func (d *Dataset) Remove(id int) *Tree {
	t, ok := d.Trees[id]
	if !ok {
		return nil
	}
	i := d.pos[id]
	last := len(d.live) - 1
	d.live[i] = d.live[last]
	d.pos[d.live[i].ID] = i
	d.live = d.live[:last]
	delete(d.pos, id)

	fi := d.fpos[id]
	flast := len(d.flat) - 1
	d.flat[fi] = d.flat[flast]
	d.fpos[int(d.flat[fi].id)] = fi
	d.flat = d.flat[:flast]
	delete(d.fpos, id)

	delete(d.Trees, id)
	return t
}

// Live returns the compact live-tree slice. Do not mutate.
func (d *Dataset) Live() []*Tree { return d.live }

// LiveCount returns the number of live trees.
func (d *Dataset) LiveCount() int { return len(d.live) }

// SortedLive returns live trees sorted by id (for deterministic builds).
func (d *Dataset) SortedLive() []*Tree {
	out := append([]*Tree(nil), d.live...)
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// generateLine builds a red line, biased to actually cut some canopy.
func generateLine(rng *rand.Rand, d *Dataset, id int) *RedLine {
	if rng.Float64() < 0.72 && len(d.live) > 0 {
		t := d.live[rng.Intn(len(d.live))]
		x := t.X + (rng.Float64()-0.5)*500
		y := t.Y + (rng.Float64()-0.5)*500
		w := 15 + rng.Float64()*260
		h := 15 + rng.Float64()*260
		return &RedLine{ID: id, X0: round2(x), Y0: round2(y),
			X1: round2(x + w), Y1: round2(y + h), Label: "施工红线"}
	}
	x := rng.Float64() * 19000
	y := rng.Float64() * 19000
	w := 20 + rng.Float64()*400
	h := 20 + rng.Float64()*400
	return &RedLine{ID: id, X0: round2(x), Y0: round2(y),
		X1: round2(x + w), Y1: round2(y + h), Label: "施工红线"}
}

// AddRandomTree inserts a generated tree near a district or at random and
// returns it. Caller is responsible for adding it to index stores.
func (d *Dataset) AddRandomTree(rng *rand.Rand) *Tree {
	var x, y float64
	if rng.Float64() < 0.8 {
		c := d.Clusters[rng.Intn(len(d.Clusters))]
		x = c.X + rng.NormFloat64()*c.Sigma*1.3
		y = c.Y + rng.NormFloat64()*c.Sigma*1.3
	} else {
		x = rng.Float64() * 19800
		y = rng.Float64() * 19800
	}
	x = math.Max(5, math.Min(19995, x))
	y = math.Max(5, math.Min(19995, y))
	t := d.NewTree(rng, x, y)
	d.add(t)
	return t
}

// RandomTree returns a random live tree.
func (d *Dataset) RandomTree(rng *rand.Rand) *Tree {
	return d.live[rng.Intn(len(d.live))]
}

// RandomLine generates a fresh red line against current data.
func (d *Dataset) RandomLine(rng *rand.Rand) *RedLine {
	return generateLine(rng, d, -1)
}

// RandomQueryRect returns a query rectangle: mostly hugging a live tree so it
// selects a meaningful number of crowns, sometimes city-wide.
func (d *Dataset) RandomQueryRect(rng *rand.Rand, maxSize float64) geom.Box {
	if len(d.live) == 0 || rng.Float64() < 0.25 {
		x := rng.Float64() * (20000 - maxSize)
		y := rng.Float64() * (20000 - maxSize)
		w := 20 + rng.Float64()*(maxSize-20)
		h := 20 + rng.Float64()*(maxSize-20)
		return geom.NewBox(2, [3]float64{x, y, 0}, [3]float64{x + w, y + h, 0})
	}
	t := d.live[rng.Intn(len(d.live))]
	w := 15 + rng.Float64()*(maxSize-15)
	h := 15 + rng.Float64()*(maxSize-15)
	x := t.X - w*rng.Float64()
	y := t.Y - h*rng.Float64()
	return geom.NewBox(2, [3]float64{x, y, 0}, [3]float64{x + w, y + h, 0})
}

// RandomQueryBox3 returns a 3-D query box: a 2-D rect plus a time window.
func (d *Dataset) RandomQueryBox3(rng *rand.Rand, maxSize float64) geom.Box {
	r := d.RandomQueryRect(rng, maxSize)
	t0 := rng.Float64() * 108
	t1 := t0 + 2 + rng.Float64()*36
	return geom.NewBox(3,
		[3]float64{r.Lo[0], r.Lo[1], t0},
		[3]float64{r.Hi[0], r.Hi[1], t1})
}

// RandomPoint returns a 2-D query point biased to a live tree.
func (d *Dataset) RandomPoint(rng *rand.Rand) [3]float64 {
	if len(d.live) == 0 {
		return [3]float64{rng.Float64() * 20000, rng.Float64() * 20000, 0}
	}
	t := d.live[rng.Intn(len(d.live))]
	return [3]float64{t.X + rng.NormFloat64()*250, t.Y + rng.NormFloat64()*250, 0}
}

// RandomPoint3 returns a 3-D query point.
func (d *Dataset) RandomPoint3(rng *rand.Rand) [3]float64 {
	p := d.RandomPoint(rng)
	p[2] = rng.Float64() * 120
	return p
}

func rngShuffle(rng *rand.Rand, s []string) {
	for i := len(s) - 1; i > 0; i-- {
		j := rng.Intn(i + 1)
		s[i], s[j] = s[j], s[i]
	}
}

// Shuffled returns live trees in a deterministic pseudo-random order.
func (d *Dataset) Shuffled(seed int64) []*Tree {
	rng := rand.New(rand.NewSource(seed))
	out := append([]*Tree(nil), d.live...)
	rng.Shuffle(len(out), func(i, j int) { out[i], out[j] = out[j], out[i] })
	return out
}
