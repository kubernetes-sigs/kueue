package routine

// Wrapper is used to wrap a function that will run in a goroutine.
type Wrapper interface {
	Run(func())
}

var _ Wrapper = &wrapper{}

var DefaultWrapper Wrapper = NewWrapper(nil, nil)

// wrapper implement the Wrapper interface.
// before() will be executed before the function starts, and after()
// will be executed after the function ends.
type wrapper struct {
	before func()
	after  func()
}

func (l *wrapper) Run(f func()) {
	if l.before != nil {
		l.before()
	}
	go func() {
		if l.after != nil {
			defer l.after()
		}
		f()
	}()
}

func NewWrapper(before, after func()) Wrapper {
	return &wrapper{
		before: before,
		after:  after,
	}
}
