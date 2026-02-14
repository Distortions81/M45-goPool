package main

func almostEqualFloat64(a, b, eps float64) bool {
	if a > b {
		return a-b <= eps
	}
	return b-a <= eps
}
