package rankfit

import "math"

// gradTol is the relative gradient size minimize stops at; stallTol the
// looser one it accepts when the objective stops decreasing measurably.
const (
	gradTol  = 1e-6
	stallTol = 1e-4
)

// minimize runs BFGS with a backtracking (Armijo) line search from x0. f
// returns the objective and writes its gradient into g. It is deterministic:
// no randomness, fixed iteration order. converged is false when it stopped
// at fitMaxIter rather than at a stationary point.
func minimize(f func(x, g []float64) float64, x0 []float64) (x []float64, iters int, converged bool) {
	n := len(x0)
	x = append([]float64(nil), x0...)
	g := make([]float64, n)
	fx := f(x, g)
	if math.IsNaN(fx) || math.IsInf(fx, 0) {
		return x, 0, false
	}
	h := identity(n) // inverse Hessian estimate
	dir, xn, gn := make([]float64, n), make([]float64, n), make([]float64, n)
	for iters = 0; iters < fitMaxIter; iters++ {
		if maxAbs(g) < gradTol*max(1, math.Abs(fx)) {
			return x, iters, true
		}
		for i := range n {
			dir[i] = 0
			for j := range n {
				dir[i] -= h[i][j] * g[j]
			}
		}
		slope := dot(g, dir)
		if !(slope < 0) { // not a descent direction: restart from steepest descent
			h = identity(n)
			for i := range n {
				dir[i] = -g[i]
			}
			slope = dot(g, dir)
		}
		step, fn := 1.0, 0.0
		for ; step > 1e-14; step /= 2 {
			for i := range n {
				xn[i] = x[i] + step*dir[i]
			}
			fn = f(xn, gn)
			if !math.IsNaN(fn) && fn <= fx+1e-4*step*slope {
				break
			}
		}
		if step <= 1e-14 {
			return x, iters, maxAbs(g) < stallTol*max(1, math.Abs(fx))
		}
		// No measurable decrease: the sum's rounding, not the optimum, now limits
		// the gradient. Stop, converged if the gradient is small by a looser test.
		stalled := fx-fn <= 1e-13*max(1, math.Abs(fx))
		// BFGS update of the inverse Hessian with s = xn-x, y = gn-g.
		s, y := make([]float64, n), make([]float64, n)
		for i := range n {
			s[i], y[i] = xn[i]-x[i], gn[i]-g[i]
		}
		if sy := dot(s, y); sy > 1e-12 {
			hy := make([]float64, n)
			for i := range n {
				for j := range n {
					hy[i] += h[i][j] * y[j]
				}
			}
			yhy := dot(y, hy)
			for i := range n {
				for j := range n {
					h[i][j] += (sy+yhy)*s[i]*s[j]/(sy*sy) - (hy[i]*s[j]+s[i]*hy[j])/sy
				}
			}
		}
		copy(x, xn)
		copy(g, gn)
		fx = fn
		if stalled {
			return x, iters + 1, maxAbs(g) < stallTol*max(1, math.Abs(fx))
		}
	}
	return x, iters, maxAbs(g) < gradTol*max(1, math.Abs(fx))
}

func identity(n int) [][]float64 {
	h := make([][]float64, n)
	for i := range h {
		h[i] = make([]float64, n)
		h[i][i] = 1
	}
	return h
}

func dot(a, b []float64) float64 {
	s := 0.0
	for i := range a {
		s += a[i] * b[i]
	}
	return s
}

func maxAbs(v []float64) float64 {
	m := 0.0
	for _, x := range v {
		m = max(m, math.Abs(x))
	}
	return m
}
