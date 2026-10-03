package jurisdiction

import (
	"fmt"
	"math/big"
	"sort"
	"time"

	"github.com/zoikogroup/zoikotax/backend/internal/platform/canonical"
)

// Point is a coordinate pair as canonical decimal strings (ADR-0001 C1: a
// coordinate that picks the wrong side of a boundary picks the wrong tax, so
// it is never a float).
type Point struct {
	Latitude  string
	Longitude string
}

// rat is a point in exact rational coordinates: x is longitude, y latitude.
type rat struct{ x, y *big.Rat }

func (p Point) exact() (rat, error) {
	lat, ok := new(big.Rat).SetString(p.Latitude)
	if !ok {
		return rat{}, fmt.Errorf("jurisdiction: latitude %q is not a decimal", p.Latitude)
	}
	lon, ok := new(big.Rat).SetString(p.Longitude)
	if !ok {
		return rat{}, fmt.Errorf("jurisdiction: longitude %q is not a decimal", p.Longitude)
	}
	if lat.Cmp(big.NewRat(-90, 1)) < 0 || lat.Cmp(big.NewRat(90, 1)) > 0 {
		return rat{}, fmt.Errorf("jurisdiction: latitude %s is outside [-90, 90]", p.Latitude)
	}
	if lon.Cmp(big.NewRat(-180, 1)) < 0 || lon.Cmp(big.NewRat(180, 1)) > 0 {
		return rat{}, fmt.Errorf("jurisdiction: longitude %s is outside [-180, 180]", p.Longitude)
	}
	return rat{x: lon, y: lat}, nil
}

// Validate refuses a point that is not two in-range decimals.
func (p Point) Validate() error {
	_, err := p.exact()
	return err
}

// Canonical renders a point for evidence.
func (p Point) Canonical() canonical.Value {
	return canonical.Object(
		canonical.F("latitude", canonical.String(p.Latitude)),
		canonical.F("longitude", canonical.String(p.Longitude)),
	)
}

// Ring is a closed ring of points; the last point joins the first.
type Ring []Point

// Polygon is an outer ring less any holes.
type Polygon struct {
	Outer Ring
	Holes []Ring
}

// Boundary is one jurisdiction's area in a dataset.
type Boundary struct {
	Jurisdiction ID
	Level        Level
	Polygons     []Polygon
}

// DatasetRef names one version of a boundary dataset, and is what a spatial
// resolution records (JUR-REQ-0003) and a replay resolves against
// (JUR-REQ-0004).
type DatasetRef struct {
	ID      string
	Version string
	Digest  canonical.Digest
}

// String renders the id@version form a situs rule pins, e.g.
// boundary:us-ca@2026.02.
func (r DatasetRef) String() string { return r.ID + "@" + r.Version }

// BoundaryDataset is a versioned, immutable set of boundaries
// (JUR-001 §2.5). Every field is unexported and every accessor copies: a
// dataset cannot be corrected in place, only superseded by a new version
// (JUR-REQ-0005), and its digest is computed over its content at
// construction so a later difference is detectable.
type BoundaryDataset struct {
	ref        DatasetRef
	published  time.Time
	source     string
	boundaries []Boundary
}

// NewBoundaryDataset validates and seals a dataset version.
func NewBoundaryDataset(id, version, source string, published time.Time, boundaries []Boundary) (*BoundaryDataset, error) {
	switch {
	case id == "" || version == "":
		return nil, fmt.Errorf("jurisdiction: a boundary dataset needs an id and a version")
	case source == "":
		return nil, fmt.Errorf("jurisdiction: boundary dataset %s@%s attributes no source", id, version)
	case published.IsZero():
		return nil, fmt.Errorf("jurisdiction: boundary dataset %s@%s has no publication date", id, version)
	}
	bs := make([]Boundary, len(boundaries))
	seen := map[ID]bool{}
	for i, b := range boundaries {
		if err := b.Jurisdiction.Validate(); err != nil {
			return nil, err
		}
		if seen[b.Jurisdiction] {
			return nil, fmt.Errorf("jurisdiction: dataset %s@%s carries %s twice", id, version, b.Jurisdiction)
		}
		seen[b.Jurisdiction] = true
		if !b.Level.Valid() {
			return nil, fmt.Errorf("jurisdiction: dataset %s@%s gives %s level %q", id, version, b.Jurisdiction, b.Level)
		}
		if len(b.Polygons) == 0 {
			return nil, fmt.Errorf("jurisdiction: dataset %s@%s gives %s no polygon", id, version, b.Jurisdiction)
		}
		bs[i] = cloneBoundary(b)
		for _, poly := range bs[i].Polygons {
			for _, ring := range append([]Ring{poly.Outer}, poly.Holes...) {
				if len(ring) < 3 {
					return nil, fmt.Errorf("jurisdiction: dataset %s@%s: %s has a ring of %d points", id, version, b.Jurisdiction, len(ring))
				}
				for _, p := range ring {
					if err := p.Validate(); err != nil {
						return nil, err
					}
				}
			}
		}
	}
	sort.Slice(bs, func(i, j int) bool { return bs[i].Jurisdiction < bs[j].Jurisdiction })
	d := &BoundaryDataset{
		ref: DatasetRef{ID: id, Version: version}, published: published.UTC(), source: source, boundaries: bs,
	}
	digest, err := canonical.Sum(d.canonical())
	if err != nil {
		return nil, fmt.Errorf("jurisdiction: digest dataset %s@%s: %w", id, version, err)
	}
	d.ref.Digest = digest
	return d, nil
}

func cloneBoundary(b Boundary) Boundary {
	out := Boundary{Jurisdiction: b.Jurisdiction, Level: b.Level, Polygons: make([]Polygon, len(b.Polygons))}
	for i, p := range b.Polygons {
		out.Polygons[i].Outer = append(Ring(nil), p.Outer...)
		for _, h := range p.Holes {
			out.Polygons[i].Holes = append(out.Polygons[i].Holes, append(Ring(nil), h...))
		}
	}
	return out
}

// Ref returns the dataset's identity, version and digest.
func (d *BoundaryDataset) Ref() DatasetRef { return d.ref }

// Published returns the publication date.
func (d *BoundaryDataset) Published() time.Time { return d.published }

// Source returns the source attribution.
func (d *BoundaryDataset) Source() string { return d.source }

// Boundaries returns a copy of the dataset's boundaries, sorted by id.
func (d *BoundaryDataset) Boundaries() []Boundary {
	out := make([]Boundary, len(d.boundaries))
	for i, b := range d.boundaries {
		out[i] = cloneBoundary(b)
	}
	return out
}

// Containing returns every boundary that contains p, sorted by id. A point on
// an edge is inside: boundaries are closed sets, and with exact arithmetic
// "on the edge" is a fact rather than a floating-point accident, so it is
// answered the same way on every replica.
func (d *BoundaryDataset) Containing(p Point) ([]Boundary, error) {
	q, err := p.exact()
	if err != nil {
		return nil, err
	}
	var out []Boundary
	for _, b := range d.boundaries {
		in, err := b.contains(q)
		if err != nil {
			return nil, err
		}
		if in {
			out = append(out, cloneBoundary(b))
		}
	}
	return out, nil
}

func (b Boundary) contains(q rat) (bool, error) {
	for _, poly := range b.Polygons {
		in, err := ringContains(poly.Outer, q, true)
		if err != nil || !in {
			if err != nil {
				return false, err
			}
			continue
		}
		inHole := false
		for _, h := range poly.Holes {
			// A point on a hole's edge belongs to the polygon: the hole is
			// open, the polygon closed.
			strictly, err := ringContains(h, q, false)
			if err != nil {
				return false, err
			}
			if strictly {
				inHole = true
				break
			}
		}
		if !inHole {
			return true, nil
		}
	}
	return false, nil
}

// ringContains is the even-odd ray cast, exact. onEdge says what a point on
// the ring itself counts as.
func ringContains(ring Ring, q rat, onEdge bool) (bool, error) {
	pts := make([]rat, len(ring))
	for i, p := range ring {
		r, err := p.exact()
		if err != nil {
			return false, err
		}
		pts[i] = r
	}
	inside := false
	for i := range pts {
		a, b := pts[i], pts[(i+1)%len(pts)]
		if onSegment(a, b, q) {
			return onEdge, nil
		}
		// Does the edge straddle the horizontal line through q?
		if (a.y.Cmp(q.y) > 0) != (b.y.Cmp(q.y) > 0) {
			// x where the edge meets that line: a.x + (q.y-a.y)(b.x-a.x)/(b.y-a.y)
			num := new(big.Rat).Mul(new(big.Rat).Sub(q.y, a.y), new(big.Rat).Sub(b.x, a.x))
			x := new(big.Rat).Add(a.x, new(big.Rat).Quo(num, new(big.Rat).Sub(b.y, a.y)))
			if q.x.Cmp(x) < 0 {
				inside = !inside
			}
		}
	}
	return inside, nil
}

func onSegment(a, b, q rat) bool {
	// Collinear: (b-a) x (q-a) == 0.
	cross := new(big.Rat).Sub(
		new(big.Rat).Mul(new(big.Rat).Sub(b.x, a.x), new(big.Rat).Sub(q.y, a.y)),
		new(big.Rat).Mul(new(big.Rat).Sub(b.y, a.y), new(big.Rat).Sub(q.x, a.x)),
	)
	if cross.Sign() != 0 {
		return false
	}
	return between(a.x, b.x, q.x) && between(a.y, b.y, q.y)
}

func between(a, b, v *big.Rat) bool {
	lo, hi := a, b
	if lo.Cmp(hi) > 0 {
		lo, hi = hi, lo
	}
	return v.Cmp(lo) >= 0 && v.Cmp(hi) <= 0
}

func (d *BoundaryDataset) canonical() canonical.Value {
	bs := make([]canonical.Value, len(d.boundaries))
	for i, b := range d.boundaries {
		polys := make([]canonical.Value, len(b.Polygons))
		for j, p := range b.Polygons {
			holes := make([]canonical.Value, len(p.Holes))
			for k, h := range p.Holes {
				holes[k] = ringCanonical(h)
			}
			polys[j] = canonical.Object(
				canonical.F("outer", ringCanonical(p.Outer)),
				canonical.F("holes", canonical.Array(holes...)),
			)
		}
		bs[i] = canonical.Object(
			canonical.F("jurisdiction", canonical.String(string(b.Jurisdiction))),
			canonical.F("level", canonical.String(string(b.Level))),
			canonical.F("polygons", canonical.Array(polys...)),
		)
	}
	return canonical.Object(
		canonical.F("id", canonical.String(d.ref.ID)),
		canonical.F("version", canonical.String(d.ref.Version)),
		canonical.F("source", canonical.String(d.source)),
		canonical.F("published", canonical.Time(d.published)),
		canonical.F("boundaries", canonical.Array(bs...)),
	)
}

func ringCanonical(r Ring) canonical.Value {
	pts := make([]canonical.Value, len(r))
	for i, p := range r {
		pts[i] = p.Canonical()
	}
	return canonical.Array(pts...)
}
