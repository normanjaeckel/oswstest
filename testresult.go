package main

import (
	"fmt"
	"time"
)

type TestResult struct {
	values      []time.Duration
	errors      []error
	description string
}

func (t *TestResult) Add(value time.Duration) {
	t.values = append(t.values, value)
}

func (t *TestResult) AddError(err error) {
	t.errors = append(t.errors, err)
}

func (t *TestResult) String() string {
	s := fmt.Sprintf(
		"%s\ncount: %d\nmin: %dms\nmax: %dms\nave: %dms\n",
		t.description,
		t.Count(),
		t.min()/time.Millisecond,
		t.max()/time.Millisecond,
		t.ave()/time.Millisecond,
	)
	if len(t.errors) > 0 {
		s += fmt.Sprintf("error count: %d\n", len(t.errors))
		if ShowAllErros {
			for i, err := range t.errors {
				s += fmt.Sprintf("%3d error: %s\n", i+1, err)
			}
		} else {
			s += fmt.Sprintf("first error: %s\n", t.errors[0])
		}
	}
	return s
}

func (t *TestResult) Count() int {
	return len(t.values)
}

func (t *TestResult) ErrCount() int {
	return len(t.errors)
}

func (t *TestResult) CountBoth() int {
	return t.Count() + t.ErrCount()
}

func (t *TestResult) min() (m time.Duration) {
	for i, v := range t.values {
		if i == 0 {
			m = v
			continue
		}
		if v < m {
			m = v
		}
	}
	return m
}

func (t *TestResult) max() (m time.Duration) {
	for _, v := range t.values {
		if v > m {
			m = v
		}
	}
	return m
}

func (t *TestResult) ave() (m time.Duration) {
	var a time.Duration
	for _, v := range t.values {
		a += v
	}
	if len(t.values) == 0 {
		return 0
	}
	return time.Duration(a.Nanoseconds() / int64(len(t.values)))
}
