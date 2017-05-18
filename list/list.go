package list

import (
	"errors"
	"strconv"
	"strings"
	"unicode"

	"github.com/ryanuber/go-glob"
)

func Glob(pattern, subj string) (bool, error) {
	patterns, err := Expand(pattern)
	if err != nil {
		return false, err
	}
	for _, p := range patterns {
		if glob.Glob(p, subj) {
			return true, nil
		}
	}
	return false, nil
}

// expands standard glob syntax {n..m,a,b,c} to slice of strings
func Expand(pattern string) (res []string, err error) {

	// computing start/end offsets of { ... } block
	found, start, end, err := computeOffsets(pattern)
	if err != nil {
		return
	}
	if !found {
		res, err = expandCommas("", pattern, "")
		return
	}

	// transforming all "n..m" => "n,n+1,n+2,...,m"
	values, err := intervalsToCommas(pattern[start+1 : end])
	if err != nil {
		return
	}

	// expanding all {n,m,k,...} to slice [ n m k ... ]
	expanded, err := expandCommas(pattern[:start], values, pattern[end+1:])
	if err != nil {
		return
	}

	res = make([]string, 0, 10)
	for _, line := range expanded {
		var tmpRes []string
		tmpRes, err = Expand(line)
		if err != nil {
			return
		}
		res = append(res, tmpRes...)
	}
	return
}

func computeOffsets(pattern string) (found bool, start, end int, err error) {
	start = strings.IndexRune(pattern, '{')
	if start == -1 {
		return
	}
	end = strings.IndexRune(pattern, '}')
	if end == -1 {
		err = errors.New("No terminating '}' found in ")
		return
	}
	if end-start < 2 {
		err = errors.New("Empty pattern between {}")
		return
	}
	found = true
	return
}

func intervalsToCommas(values string) (result string, err error) {
	result = values
	for {
		found, expandedInt, expandErr := expandInterval(result)
		if expandErr != nil {
			err = expandErr
			return
		}
		if !found {
			break
		}
		result = expandedInt
	}
	return
}

func expandInterval(pattern string) (found bool, r string, err error) {

	// delimiter offset
	do := strings.Index(pattern, "..")
	if do == -1 {
		return
	}

	start, before, err := computeIntervalStart(pattern, do)
	if err != nil {
		return
	}

	end, after, err := computeIntervalEnd(pattern, do)
	if err != nil {
		return
	}

	if start > end {
		start, end = end, start
	}

	r = before
	for i := start; i <= end; i++ {
		r = r + strconv.Itoa(i) + ","
	}
	r = strings.TrimRight(r, ",") + after

	found = true
	return
}

func computeIntervalStart(pattern string, do int) (start int, before string, err error) {

	startRight := do - 1
	startLeft := startRight

	for i := startLeft; i >= 0; i-- {
		if !unicode.IsNumber(rune(pattern[i])) {
			break
		}
		startLeft--
	}
	if startLeft == startRight {
		err = errors.New("No digits before '..'")
		return
	}
	before = pattern[:startLeft+1]
	start, err = strconv.Atoi(pattern[startLeft+1 : startRight+1])
	return
}

func computeIntervalEnd(pattern string, do int) (end int, after string, err error) {

	endLeft := do + 2
	endRight := endLeft

	for i := endRight; i < len(pattern); i++ {
		cr := rune(pattern[i])
		if !unicode.IsNumber(cr) {
			if cr != ',' {
				err = errors.New("Found non-comma terminating character")
				return
			}
			break
		}
		endRight++
	}
	if endRight == endLeft {
		err = errors.New("No digits after '..'")
		return
	}
	after = pattern[endRight:]
	end, err = strconv.Atoi(pattern[endLeft:endRight])
	return
}

func expandCommas(before, p, after string) (r []string, err error) {
	r = make([]string, 0, 10)
	var start, i int
	for i = 0; i < len(p); i++ {
		if rune(p[i]) != ',' {
			continue
		}
		if start == i {
			err = errors.New("No character after comma")
			return
		}
		r = append(r, before+p[start:i]+after)
		start = i + 1
	}
	if start == i {
		err = errors.New("No character after comma")
		return
	}
	r = append(r, before+p[start:i]+after)
	return
}
