package misc

// Canonical port of src/misc/dbl.c — my_str2double / my_double2str.

import (
	"math"
)

// MyStr2double — C: my_str2double (misc/dbl.c:32-87). Returns the
// parsed value and the index where parsing stopped (C's *endp).
// Custom parser: whitespace bytes <33 skipped, only '-' sign, integer
// part into int, up to 16 fractional digits, 'e'/'E' exponent.
func MyStr2double(str string) (float64, int) {
	i := 0
	ret := 1.0
	var n int32 // C: int
	m := 0
	var e int32 // C: int
	var o uint64

	// C: while(*str && *str < 33) — signed char, so bytes ≥ 0x80
	// are negative and also count as "whitespace".
	for i < len(str) && int8(str[i]) != 0 && int8(str[i]) < 33 {
		i++
	}

	if i < len(str) && str[i] == '-' {
		ret = -1.0
		i++
	}

	for i < len(str) && str[i] >= '0' && str[i] <= '9' {
		n = n*10 + int32(str[i]-'0')
		i++
	}

	if i >= len(str) || str[i] != '.' {
		ret *= float64(n)
	} else {
		i++
		for i < len(str) && str[i] >= '0' && str[i] <= '9' {
			if m > -16 {
				o = o*10 + uint64(str[i]) - '0'
				m--
			}
			i++
		}
		ret *= float64(n) + math.Pow(10, float64(m))*float64(o)
	}

	if i < len(str) && (str[i] == 'e' || str[i] == 'E') {
		var esign int32 = 1
		i++
		if i < len(str) && str[i] == '+' {
			i++
		} else if i < len(str) && str[i] == '-' {
			i++
			esign = -1
		}
		for i < len(str) && str[i] >= '0' && str[i] <= '9' {
			e = e*10 + int32(str[i]-'0')
			i++
		}
		ret *= math.Pow(10, float64(e*esign))
	}

	return ret, i
}

// getdigit — C: getdigit (misc/dbl.c:149-159).
func jsonGetdigit(val *float64, cnt *int) byte {
	if *cnt >= 16 {
		*cnt++
		return '0'
	}
	*cnt++
	digit := int(*val)
	d := float64(digit)
	*val = (*val - d) * 10.0
	return byte(digit) + '0'
}

const (
	xGENERIC = 0
	xFLOAT   = 1
	xEXP     = 2
)

// MyDouble2str — C: my_double2str (misc/dbl.c:165-280). %g-style
// formatting: default precision 20 (capped at bufsize/2-10), exponent
// form when exp < -4 or exp > precision, trailing zeros stripped.
// Returns the formatted string (buf always NUL-terminated in C).
func MyDouble2str(bufsize int, realvalue float64) string {
	if bufsize < 8 {
		return ""
	}

	precision := min(20, bufsize/2-10)

	var prefix byte
	if realvalue < 0.0 {
		realvalue = -realvalue
		prefix = '-'
	}

	xtype := xGENERIC
	if xtype == xGENERIC && precision > 0 {
		precision--
	}

	rounder := 0.5
	for idx := precision; idx > 0; idx-- {
		rounder *= 0.1
	}

	exp := 0
	if math.IsNaN(realvalue) {
		return "NaN"
	}

	if realvalue > 0.0 {
		for realvalue >= 1e32 && exp <= 350 {
			realvalue *= 1e-32
			exp += 32
		}
		for realvalue >= 1e8 && exp <= 350 {
			realvalue *= 1e-8
			exp += 8
		}
		for realvalue >= 10.0 && exp <= 350 {
			realvalue *= 0.1
			exp++
		}
		for realvalue < 1e-8 {
			realvalue *= 1e8
			exp -= 8
		}
		for realvalue < 1.0 {
			realvalue *= 10.0
			exp--
		}
		if exp > 350 {
			if prefix == '-' {
				return "-Inf"
			}
			return "Inf"
		}
	}

	flagExp := xtype == xEXP
	if xtype != xFLOAT {
		realvalue += rounder
		if realvalue >= 10.0 {
			realvalue *= 0.1
			exp++
		}
	}
	var flagRtz bool
	if xtype == xGENERIC {
		flagRtz = true // !flag_alternateform (0 in C)
		if exp < -4 || exp > precision {
			xtype = xEXP
		} else {
			precision = precision - exp
			xtype = xFLOAT
		}
	}

	var e2 int
	if xtype == xEXP {
		e2 = 0
	} else {
		e2 = exp
	}
	nsd := 0
	flagDp := precision > 0 // | flag_alternateform | flag_altform2 (0)

	buf := make([]byte, 0, bufsize)

	if prefix != 0 {
		buf = append(buf, prefix)
	}
	if e2 < 0 {
		buf = append(buf, '0')
	} else {
		for ; e2 >= 0; e2-- {
			buf = append(buf, jsonGetdigit(&realvalue, &nsd))
		}
	}
	if flagDp {
		buf = append(buf, '.')
	}
	for e2++; e2 < 0; e2++ {
		buf = append(buf, '0')
		precision--
	}
	for precision > 0 {
		precision--
		buf = append(buf, jsonGetdigit(&realvalue, &nsd))
	}

	// Remove trailing zeros and a bare "."
	if flagRtz && flagDp {
		for len(buf) > 0 && buf[len(buf)-1] == '0' {
			buf = buf[:len(buf)-1]
		}
		if len(buf) > 0 && buf[len(buf)-1] == '.' {
			buf = buf[:len(buf)-1]
		}
	}

	if flagExp || xtype == xEXP {
		buf = append(buf, 'e')
		if exp < 0 {
			buf = append(buf, '-')
			exp = -exp
		} else {
			buf = append(buf, '+')
		}
		if exp >= 100 {
			buf = append(buf, byte(exp/100)+'0')
			exp %= 100
		}
		buf = append(buf, byte(exp/10)+'0')
		buf = append(buf, byte(exp%10)+'0')
	}
	return string(buf)
}
