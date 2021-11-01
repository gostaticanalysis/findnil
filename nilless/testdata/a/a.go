package main

import "go.uber.org/zap"

type T struct {
	N int
	m *int
}

var gt *T //@ isZero

func main() {
	f(new(T))
	f(g())
	println(gt.N)
	var err error //@ isNil
	println(err.Error())
	var logger *zap.Logger //@ isNil
	println(logger.Info)
}

func f(t *T) {
	println(t.N)
}

func g() *T {
	return nil //@ isNil
}
