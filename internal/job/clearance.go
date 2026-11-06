package job

import (
	"bufio"
	"bytes"
	"fmt"
	"math"
	"strconv"
	"strings"
)

// Clearance records what was read from first-layer object toolpaths, in bed coordinates.
type Clearance struct {
	Verified   bool    `json:"verified"`
	Extrusions int     `json:"object_extrusions"`
	Conflicts  int     `json:"conflicting_extrusions"`
	MarginMM   float64 `json:"margin_mm"`
	Reason     string  `json:"reason,omitempty"`
}

type (
	point     struct{ x, y float64 }
	rectangle struct{ min, max point }
)

// X1C start lines occupy the front strip and front-left exclusion corner.
// The 1 mm margin also covers the width of an extrusion around its centreline.
var startZones = []rectangle{
	{point{17, -1}, point{241, 13}},
	{point{-1, -1}, point{19, 29}},
}

// FirstLayerClearance follows Bambu Studio object markers and G0/G1/G2/G3 paths.
// Startup, travel, retraction, and prime-tower/custom G-code are not object extrusion.
// Unsupported object motion stays unverified instead of claiming clear space.
func FirstLayerClearance(gcode []byte) Clearance {
	out := Clearance{MarginMM: 1}
	pos := point{math.NaN(), math.NaN()}
	e := 0.0
	relative, relativeE := false, false
	millimeters, xyPlane, relativeArc := true, true, true
	layer, object := 0, false
	scanner := bufio.NewScanner(bytes.NewReader(gcode))
	scanner.Buffer(make([]byte, 4096), 1<<20)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		switch {
		case strings.HasPrefix(line, "; layer num/total_layer_count:"):
			n, _, _ := strings.Cut(strings.TrimSpace(strings.TrimPrefix(line, "; layer num/total_layer_count:")), "/")
			layer, _ = strconv.Atoi(n)
			object = false
		case strings.HasPrefix(line, "; OBJECT_ID:"):
			id, err := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(line, "; OBJECT_ID:")))
			object = err == nil && id >= 0
		case strings.HasPrefix(line, "; FEATURE: Custom"), strings.HasPrefix(line, "; FEATURE: Prime tower"):
			object = false
		}
		code, _, _ := strings.Cut(line, ";")
		fields := strings.Fields(code)
		if len(fields) == 0 {
			continue
		}
		switch fields[0] {
		case "G20":
			millimeters = false
			continue
		case "G21":
			millimeters = true
			continue
		case "G17":
			xyPlane = true
			continue
		case "G18", "G19":
			xyPlane = false
			continue
		case "G90.1":
			relativeArc = false
			continue
		case "G91.1":
			relativeArc = true
			continue
		case "G90":
			relative = false
			continue
		case "G91":
			relative = true
			continue
		case "M82":
			relativeE = false
			continue
		case "M83":
			relativeE = true
			continue
		}
		if fields[0] != "G0" && fields[0] != "G1" && fields[0] != "G2" && fields[0] != "G3" && fields[0] != "G92" {
			continue
		}
		args := map[byte]float64{}
		for _, f := range fields[1:] {
			if len(f) < 2 {
				continue
			}
			v, err := strconv.ParseFloat(f[1:], 64)
			if err == nil && !math.IsInf(v, 0) && !math.IsNaN(v) {
				args[f[0]] = v
			} else if layer == 1 && object && strings.ContainsRune("XYEIJR", rune(f[0])) {
				out.Reason = "object motion has invalid numeric coordinates"
			}
		}
		if fields[0] == "G92" {
			if v, ok := args['X']; ok {
				pos.x = v
				out.Reason = "G92 XY coordinate offsets are unsupported"
			}
			if v, ok := args['Y']; ok {
				pos.y = v
				out.Reason = "G92 XY coordinate offsets are unsupported"
			}
			if v, ok := args['E']; ok {
				e = v
			}
			continue
		}
		next := pos
		if x, ok := args['X']; ok {
			next.x = x
			if relative {
				next.x += pos.x
			}
		}
		if y, ok := args['Y']; ok {
			next.y = y
			if relative {
				next.y += pos.y
			}
		}
		extrusion := 0.0
		if v, ok := args['E']; ok {
			extrusion = v
			if !relativeE {
				extrusion -= e
				e = v
			} else {
				e += v
			}
		}
		arc := fields[0] == "G2" || fields[0] == "G3"
		if layer == 1 && object && extrusion > 0 && (next != pos || arc) {
			out.Extrusions++
			if !millimeters || (arc && (!xyPlane || !relativeArc)) {
				out.Reason = "object motion needs millimetres and XY arcs with relative I/J centres"
			} else if math.IsNaN(pos.x) || math.IsNaN(pos.y) || math.IsNaN(next.x) || math.IsNaN(next.y) {
				out.Reason = "object extrusion has unknown XY coordinates"
			} else {
				hit, err := pathHitsZones(pos, next, args, fields[0])
				if err != nil {
					out.Reason = err.Error()
				}
				if hit {
					out.Conflicts++
				}
			}
		}
		pos = next
	}
	if err := scanner.Err(); err != nil {
		out.Reason = "cannot read all G-code lines"
	}
	if out.Extrusions == 0 && out.Reason == "" {
		out.Reason = "no first-layer object extrusions found (layer/OBJECT_ID markers required)"
	}
	out.Verified = out.Extrusions > 0 && out.Reason == ""
	return out
}

func pathHitsZones(a, b point, args map[byte]float64, command string) (bool, error) {
	if command == "G0" || command == "G1" {
		for _, zone := range startZones {
			if segmentHits(a, b, zone) {
				return true, nil
			}
		}
		return false, nil
	}
	_, hasI := args['I']
	_, hasJ := args['J']
	if _, hasR := args['R']; hasR || (!hasI && !hasJ) {
		return false, fmt.Errorf("object arc needs relative I/J centre offsets")
	}
	center := point{a.x + args['I'], a.y + args['J']}
	radius := math.Hypot(a.x-center.x, a.y-center.y)
	if radius == 0 || math.Abs(math.Hypot(b.x-center.x, b.y-center.y)-radius) > math.Max(.05, radius*.01) {
		return false, fmt.Errorf("object arc has inconsistent centre/radius")
	}
	start, end := math.Atan2(a.y-center.y, a.x-center.x), math.Atan2(b.y-center.y, b.x-center.x)
	onArc := func(p point) bool {
		angle := math.Atan2(p.y-center.y, p.x-center.x)
		if a == b {
			return true
		}
		if command == "G2" {
			return angleDistance(angle, start) <= angleDistance(end, start)+1e-8
		}
		return angleDistance(start, angle) <= angleDistance(start, end)+1e-8
	}
	for _, zone := range startZones {
		if inside(a, zone) || inside(b, zone) {
			return true, nil
		}
		for _, x := range []float64{zone.min.x, zone.max.x} {
			d := radius*radius - (x-center.x)*(x-center.x)
			if d >= 0 {
				for _, y := range []float64{center.y - math.Sqrt(d), center.y + math.Sqrt(d)} {
					p := point{x, y}
					if inside(p, zone) && onArc(p) {
						return true, nil
					}
				}
			}
		}
		for _, y := range []float64{zone.min.y, zone.max.y} {
			d := radius*radius - (y-center.y)*(y-center.y)
			if d >= 0 {
				for _, x := range []float64{center.x - math.Sqrt(d), center.x + math.Sqrt(d)} {
					p := point{x, y}
					if inside(p, zone) && onArc(p) {
						return true, nil
					}
				}
			}
		}
	}
	return false, nil
}

func angleDistance(from, to float64) float64 { return math.Mod(to-from+2*math.Pi, 2*math.Pi) }
func inside(p point, r rectangle) bool {
	return p.x >= r.min.x-1e-8 && p.x <= r.max.x+1e-8 && p.y >= r.min.y-1e-8 && p.y <= r.max.y+1e-8
}

func segmentHits(a, b point, r rectangle) bool {
	lo, hi := 0.0, 1.0
	for _, axis := range [][4]float64{{a.x, b.x, r.min.x, r.max.x}, {a.y, b.y, r.min.y, r.max.y}} {
		delta := axis[1] - axis[0]
		if delta == 0 {
			if axis[0] < axis[2] || axis[0] > axis[3] {
				return false
			}
			continue
		}
		x, y := (axis[2]-axis[0])/delta, (axis[3]-axis[0])/delta
		if x > y {
			x, y = y, x
		}
		lo = math.Max(lo, x)
		hi = math.Min(hi, y)
		if lo > hi {
			return false
		}
	}
	return true
}
