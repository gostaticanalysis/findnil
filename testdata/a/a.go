package main

type T struct {
	N int
	m *int
}

var gt *T

func main() {
	f(new(T))
	f(g())
	println(gt.N)
	var t *T
	println(t.N)
	t2 := h(2)
	println(t2.N)
	var err error
	println(err.Error())
}

func f(t *T) {
	println(t.N)
}

func g() *T {
	return nil
}

func h(n int) *T {
	if n % 2 == 0 {
		return gt
	}
	return new(T)
}
