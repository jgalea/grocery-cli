package main

import (
	"bufio"
	"fmt"
	"io"
	"math"
	"os"
	"strconv"
	"strings"
)

type basketLine struct {
	id  string
	qty float64
}

func collectLines(file string, positional []string) ([]string, error) {
	if file == "" {
		var out []string
		for _, a := range positional {
			if s := strings.TrimSpace(a); s != "" {
				out = append(out, s)
			}
		}
		return out, nil
	}
	r, closer, err := openSource(file)
	if err != nil {
		return nil, err
	}
	defer closer()
	var out []string
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		if line := stripComment(sc.Text()); line != "" {
			out = append(out, line)
		}
	}
	return out, sc.Err()
}

func collectBasket(file string, positional []string) ([]basketLine, error) {
	if file == "" {
		var out []basketLine
		for _, a := range positional {
			if s := strings.TrimSpace(a); s != "" {
				out = append(out, basketLine{id: s, qty: 1})
			}
		}
		return out, nil
	}
	r, closer, err := openSource(file)
	if err != nil {
		return nil, err
	}
	defer closer()
	var out []basketLine
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := stripComment(sc.Text())
		if line == "" {
			continue
		}
		f := strings.Fields(line)
		bl := basketLine{id: f[0], qty: 1}
		if len(f) > 1 {
			if q, err := strconv.ParseFloat(f[1], 64); err == nil && q > 0 {
				bl.qty = q
			}
		}
		out = append(out, bl)
	}
	return out, sc.Err()
}

func openSource(file string) (io.Reader, func(), error) {
	if file == "-" {
		return os.Stdin, func() {}, nil
	}
	f, err := os.Open(file)
	if err != nil {
		return nil, nil, err
	}
	return f, func() { f.Close() }, nil
}

func stripComment(s string) string {
	if i := strings.IndexByte(s, '#'); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}

func cents(f float64) int64              { return int64(math.Round(f * 100)) }
func lineCents(u int64, q float64) int64 { return int64(math.Round(float64(u) * q)) }

func centsStr(c int64) string {
	neg := c < 0
	if neg {
		c = -c
	}
	s := fmt.Sprintf("%d.%02d", c/100, c%100)
	if neg {
		return "-" + s
	}
	return s
}

func fmtQty(q float64) string {
	if q == math.Trunc(q) {
		return strconv.FormatInt(int64(q), 10)
	}
	return strconv.FormatFloat(q, 'f', -1, 64)
}

func plural(n int, one, many string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, one)
	}
	return fmt.Sprintf("%d %s", n, many)
}
