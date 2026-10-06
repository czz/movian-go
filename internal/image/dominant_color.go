package image

// Canonical port of src/image/dominantcolor.c — k-means dominant
// color extraction writing pm->pm_primary_color.

import "math/rand"

const (
	highThreshold = 220 // C: HIGH_THRESHOLD (dominantcolor.c:23)
	lowThreshold  = 60  // C: LOW_THRESHOLD (dominantcolor.c:22)
)

// centroid — C: centroid_t (dominantcolor.c).
type centroid struct {
	numPixels  int
	r, g, b    int
	or, og, ob int
}

// randomizeCentroids — C: randomize_centroids (dominantcolor.c:36-49).
func randomizeCentroids(c []centroid, k int) {
	for i := range k {
		c[i].r = rand.Intn(256)
		c[i].g = rand.Intn(256)
		c[i].b = rand.Intn(256)

		c[i].or = 0
		c[i].og = 0
		c[i].ob = 0
	}
}

// pixel — C: pixel_t (dominantcolor.c).
type pixel struct {
	r, g, b, c uint8
}

// extractPixelsBGR32 — C: extract_pixels_bgr32 (dominantcolor.c:57-93).
func extractPixelsBGR32(out []pixel, pm *Pixmap) int {
	num := 0
	s := 0
	for range pm.Height {
		src := s
		for range pm.Width {
			u32 := uint32(pm.Data[src]) | uint32(pm.Data[src+1])<<8 |
				uint32(pm.Data[src+2])<<16 | uint32(pm.Data[src+3])<<24
			src += 4

			pR := int(u32 & 0xff)
			pG := int((u32 >> 8) & 0xff)
			pB := int((u32 >> 16) & 0xff)
			pA := int((u32 >> 24) & 0xff)

			if pA < 128 {
				continue
			}

			if pR > highThreshold && pG > highThreshold && pB > highThreshold {
				continue
			}

			if pR < lowThreshold && pG < lowThreshold && pB < lowThreshold {
				continue
			}

			num++
			if out != nil {
				out[num-1] = pixel{uint8(pR), uint8(pG), uint8(pB), 0xff}
			}
		}
		s += pm.Stride
	}
	return num
}

// extractPixelsRGB24 — C: extract_pixels_rgb24
// (dominantcolor.c:95-126).
func extractPixelsRGB24(out []pixel, pm *Pixmap) int {
	num := 0
	s := 0
	for range pm.Height {
		src := s
		for range pm.Width {
			pR := int(pm.Data[src])
			pG := int(pm.Data[src+1])
			pB := int(pm.Data[src+2])
			src += 3

			if pR > highThreshold && pG > highThreshold && pB > highThreshold {
				continue
			}

			if pR < lowThreshold && pG < lowThreshold && pB < lowThreshold {
				continue
			}

			num++
			if out != nil {
				out[num-1] = pixel{uint8(pR), uint8(pG), uint8(pB), 0xff}
			}
		}
		s += pm.Stride
	}
	return num
}

// DominantColor — C: dominant_color (dominantcolor.c:131-235).
// Writes pm.PrimaryColor; returns nothing. (Go keeps the name used by
// callers; signature matches C's void return by writing into pm.)
func DominantColor(pm *Pixmap) {
	const K = 8
	var numPixels int
	var pixels []pixel

	switch pm.Type {
	case PixmapRGB24:
		numPixels = extractPixelsRGB24(nil, pm)
		pixels = make([]pixel, numPixels)
		extractPixelsRGB24(pixels, pm)

	case PixmapBGR32:
		numPixels = extractPixelsBGR32(nil, pm)
		pixels = make([]pixel, numPixels)
		extractPixelsBGR32(pixels, pm)

	default:
		return
	}

	centroids := make([]centroid, K)
	randomizeCentroids(centroids, K)

	for {
		for i := range numPixels {
			s := int(^uint32(0) >> 1) // C: INT32_MAX

			for j := range K {
				c := &centroids[j]

				d := (c.r-int(pixels[i].r))*(c.r-int(pixels[i].r)) +
					(c.g-int(pixels[i].g))*(c.g-int(pixels[i].g)) +
					(c.b-int(pixels[i].b))*(c.b-int(pixels[i].b))

				if d < s {
					s = d
					pixels[i].c = uint8(j)
				}
			}
		}

		for j := range K {
			c := &centroids[j]
			c.or = c.r
			c.og = c.g
			c.ob = c.b
			c.r = 0
			c.g = 0
			c.b = 0
			c.numPixels = 0
		}

		for i := range numPixels {
			c := &centroids[pixels[i].c]
			c.r += int(pixels[i].r)
			c.g += int(pixels[i].g)
			c.b += int(pixels[i].b)
			c.numPixels++
		}

		move := 0

		for j := range K {
			c := &centroids[j]
			if c.numPixels != 0 {
				c.r /= c.numPixels
				c.g /= c.numPixels
				c.b /= c.numPixels
			}

			if c.r != c.or || c.g != c.og || c.b != c.ob {
				move = 1
			}
		}
		if move == 0 {
			break
		}
	}

	var best *centroid
	for j := range K {
		c := &centroids[j]
		if best == nil || c.numPixels > best.numPixels {
			best = c
		}
	}
	if best != nil {
		pm.PrimaryColor[0] = float32(best.r) / 255.0
		pm.PrimaryColor[1] = float32(best.g) / 255.0
		pm.PrimaryColor[2] = float32(best.b) / 255.0
	}
}
