package parse

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// itemType identifies the type of lex items.
type itemType int

// Pos represents a byte position in the original input text from which
// this template was parsed.
type Pos int

// item represents a token or text string returned from the scanner.
type item struct {
	typ itemType // The type of this item.
	pos Pos      // The starting position, in bytes, of this item in the input string.
	val string   // The value of this item.
}

func (i item) String() string {
	typ := "OP"
	if t, ok := tokens[i.typ]; ok {
		typ = t
	}
	return fmt.Sprintf("%s: %.40q", typ, i.val)
}

const (
	eof                = -1
	itemError itemType = iota // error occurred; value is text of error
	itemEOF
	itemText        // plain text
	itemPlus        // plus('+')
	itemDash        // dash('-')
	itemEquals      // equals
	itemColonEquals // colon-equals (':=')
	itemColonDash   // colon-dash(':-')
	itemColonPlus   // colon-plus(':+')
	itemVariable    // variable starting with '$', such as '$hello' or '$1'
	itemLeftDelim   // left action delimiter '${'
	itemRightDelim  // right action delimiter '}'
)

var tokens = map[itemType]string{
	itemEOF:        "EOF",
	itemError:      "ERROR",
	itemText:       "TEXT",
	itemVariable:   "VAR",
	itemLeftDelim:  "START EXP",
	itemRightDelim: "END EXP",
}

// stateFn represents the state of the lexer as a function that returns the next state.
type stateFn func(*lexer) stateFn

// lexer holds the state of the scanner
type lexer struct {
	input     string    // the string being lexed
	state     stateFn   // the next lexing function to enter
	pos       Pos       // current position in the input
	start     Pos       // start position of this item
	width     Pos       // width of last rune read from input
	lastPos   Pos       // position of most recent item returned by nextItem
	items     chan item // channel of lexed items
	subsDepth int       // depth of substitution
	noDigit   bool      // if the lexer skips variables that start with a digit
}

// next returns the next rune in the input.
func (l *lexer) next() rune {
	if int(l.pos) >= len(l.input) {
		l.width = 0
		return eof
	}
	r, w := utf8.DecodeRuneInString(l.input[l.pos:])
	l.width = Pos(w)
	l.pos += l.width
	return r
}

// peek returns but does not consume the next rune in the input.
func (l *lexer) peek() rune {
	r := l.next()
	l.backup()
	return r
}

// backup steps back one rune. Can only be called once per call of next.
func (l *lexer) backup() {
	l.pos -= l.width
}

// emit passes an item back to the client.
func (l *lexer) emit(t itemType) {
	l.items <- item{t, l.start, l.input[l.start:l.pos]}
	l.lastPos = l.start
	l.start = l.pos
}

// ignore skips over the pending input before this point.
func (l *lexer) ignore() {
	l.start = l.pos
}

// errorf returns an error token and terminates the scan by passing
// back a nil pointer that will be the next state, terminating l.nextItem.
func (l *lexer) errorf(format string, args ...interface{}) stateFn {
	l.items <- item{itemError, l.start, fmt.Sprintf(format, args...)}
	return nil
}

// nextItem returns the next item from the input.
// Called by the parser, not in the lexing goroutine.
func (l *lexer) nextItem() item {
	item := <-l.items
	return item
}

// lex creates a new scanner for the input string.
func lex(input string, noDigit bool) *lexer {
	l := &lexer{
		input:   input,
		items:   make(chan item),
		noDigit: noDigit,
	}
	go l.run()
	return l
}

// run runs the state machine for the lexer.
func (l *lexer) run() {
	for l.state = lexText; l.state != nil; {
		l.state = l.state(l)
	}
	close(l.items)
}

// lexText scans until encountering with "$" or an opening action delimiter, "${".
func lexText(l *lexer) stateFn {
Loop:
	for {
		switch r := l.next(); r {
		case '$':
			l.pos--
			// emit the text we've found until here, if any.
			if l.pos > l.start {
				l.emit(itemText)
			}
			l.pos++
			switch r := l.peek(); {
			case l.noDigit && unicode.IsDigit(r):
				// ignore variable starting with digit like $1.
				l.next()
				l.emit(itemText)
			case r == '$':
				// ignore the previous '$'.
				l.ignore()
				l.next()
				l.emit(itemText)
			case r == '{':
				l.next()
				r2 := l.peek()
				if l.noDigit && unicode.IsDigit(r2) {
					// ignore variable starting with digit like ${1}.
					l.next()
					l.emit(itemText)
					break
				}
				l.subsDepth++
				l.emit(itemLeftDelim)
				return lexSubstitutionOperator
			case isAlphaNumeric(r):
				return lexVariable
			}
		case eof:
			break Loop
		}
	}
	// Correctly reached EOF.
	if l.pos > l.start {
		l.emit(itemText)
	}
	l.emit(itemEOF)
	return nil
}

// lexVariable scans a Variable: $Alphanumeric.
// The $ has been scanned.
func lexVariable(l *lexer) stateFn {
	var r rune
	for {
		r = l.next()
		if !isAlphaNumeric(r) {
			l.backup()
			break
		}
	}
	if v := l.input[l.start:l.pos]; v == "_" || v == "$_" {
		return lexText
	}
	l.emit(itemVariable)
	if l.subsDepth > 0 {
		return lexSubstitutionOperator
	}
	return lexText
}

// lexSubstitutionOperator scans a starting substitution operator (if any) and continues with lexSubstitution
func lexSubstitutionOperator(l *lexer) stateFn {
	switch r := l.next(); {
	case r == '}':
		l.subsDepth--
		l.emit(itemRightDelim)
		return lexText
	case r == eof || isEndOfLine(r):
		return l.errorf("closing brace expected")
	case isAlphaNumeric(r) && strings.HasPrefix(l.input[l.lastPos:], "${"):
		return lexVariable
	case r == '+':
		l.emit(itemPlus)
	case r == '-':
		l.emit(itemDash)
	case r == '=':
		l.emit(itemEquals)
	case r == ':':
		switch l.next() {
		case '-':
			l.emit(itemColonDash)
		case '=':
			l.emit(itemColonEquals)
		case '+':
			l.emit(itemColonPlus)
		}
	}
	return lexSubstitution
}

// lexSubstitution scans the elements inside substitution delimiters.
func lexSubstitution(l *lexer) stateFn {
	switch r := l.next(); {
	case r == '}':
		l.subsDepth--
		l.emit(itemRightDelim)
		return lexText
	case r == eof || isEndOfLine(r):
		return l.errorf("closing brace expected")
	case isAlphaNumeric(r) && strings.HasPrefix(l.input[l.lastPos:], "${"):
		fallthrough
	case r == '$':
		return lexVariable
	default:
		l.emit(itemText)
	}
	return lexSubstitution
}

// isEndOfLine reports whether r is an end-of-line character.
func isEndOfLine(r rune) bool {
	return r == '\r' || r == '\n'
}

// isAlphaNumeric reports whether r is an alphabetic, digit, or underscore.
func isAlphaNumeric(r rune) bool {
	return r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r)
}
