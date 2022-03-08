package function

// Logger is used to log function.
type Logger interface {
	Run(func())
	AsyncRun(func())
}

var _ Logger = &logger{}

var DefaultLogger Logger = NewLogger(nil, nil)

// logger implement the Logger interface.
// before() will be executed before the function starts, and after()
// will be executed after the function ends.
type logger struct {
	before func()
	after  func()
}

func (l *logger) Run(f func()) {
	if l.before != nil {
		l.before()
	}
	if l.after != nil {
		defer l.after()
	}
	f()
}

func (l *logger) AsyncRun(f func()) {
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

func NewLogger(before, after func()) Logger {
	return &logger{
		before: before,
		after:  after,
	}
}
