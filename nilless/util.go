package nilless

import (
	"math/rand"
	"strconv"
	"strings"
	"time"
)

var rnd = rand.New(rand.NewSource(time.Now().UnixNano()))

func uniqName(pattern string, f func(string) bool) string {
	prefix, suffix := pattern, ""
	if pos := strings.LastIndex(pattern, "*"); pos != -1 {
		prefix, suffix = pattern[:pos], pattern[pos+1:]
	}

	for {
		name := prefix + strconv.FormatUint(rnd.Uint64(), 10) + suffix
		if f(name) {
			return name
		}
	}
}
