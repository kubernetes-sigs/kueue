package errors

import (
	"bytes"
	"errors"
	"fmt"
	"reflect"

	"github.com/goccy/go-yaml/printer"
	"github.com/goccy/go-yaml/token"
)

const (
	defaultColorize      = false
	defaultIncludeSource = true
)

// ErrSyntax create syntax error instance with message and token
func ErrSyntax(msg string, tk *token.Token) *syntaxError {
	return &syntaxError{
		msg:   msg,
		token: tk,
	}
}

// ErrOverflow creates an overflow error instance with message and a token.
func ErrOverflow(dstType reflect.Type, num string, tk *token.Token) *overflowError {
	return &overflowError{dstType: dstType, srcNum: num, token: tk}
}

type Printer interface {
	// Print appends args to the message output.
	Print(args ...any)
}

type FormatErrorPrinter struct {
	Printer
	Colored    bool
	InclSource bool
}

var (
	As  = errors.As
	Is  = errors.Is
	New = errors.New
)

type overflowError struct {
	dstType reflect.Type
	srcNum  string
	token   *token.Token
}

func (e *overflowError) Error() string {
	return fmt.Sprintf("cannot unmarshal %s into Go value of type %s ( overflow )", e.srcNum, e.dstType)
}

func (e *overflowError) PrettyPrint(p Printer, colored, inclSource bool) error {
	return e.FormatError(&FormatErrorPrinter{Printer: p, Colored: colored, InclSource: inclSource})
}

func (e *overflowError) FormatError(p Printer) error {
	var pp printer.Printer

	var colored, inclSource bool
	if fep, ok := p.(*FormatErrorPrinter); ok {
		colored = fep.Colored
		inclSource = fep.InclSource
	}

	pos := fmt.Sprintf("[%d:%d] ", e.token.Position.Line, e.token.Position.Column)
	msg := pp.PrintErrorMessage(fmt.Sprintf("%s%s", pos, e.Error()), colored)
	if inclSource {
		msg += "\n" + pp.PrintErrorToken(e.token, colored)
	}
	p.Print(msg)

	return nil
}

type syntaxError struct {
	msg   string
	token *token.Token
}

func (e *syntaxError) PrettyPrint(p Printer, colored, inclSource bool) error {
	return e.FormatError(&FormatErrorPrinter{Printer: p, Colored: colored, InclSource: inclSource})
}

func (e *syntaxError) FormatError(p Printer) error {
	var pp printer.Printer

	var colored, inclSource bool
	if fep, ok := p.(*FormatErrorPrinter); ok {
		colored = fep.Colored
		inclSource = fep.InclSource
	}

	pos := fmt.Sprintf("[%d:%d] ", e.token.Position.Line, e.token.Position.Column)
	msg := pp.PrintErrorMessage(fmt.Sprintf("%s%s", pos, e.msg), colored)
	if inclSource {
		msg += "\n" + pp.PrintErrorToken(e.token, colored)
	}
	p.Print(msg)
	return nil
}

type PrettyPrinter interface {
	PrettyPrint(Printer, bool, bool) error
}

type Sink struct{ *bytes.Buffer }

func (es *Sink) Print(args ...interface{}) {
	fmt.Fprint(es.Buffer, args...)
}

func (es *Sink) Printf(f string, args ...interface{}) {
	fmt.Fprintf(es.Buffer, f, args...)
}

func (es *Sink) Detail() bool {
	return false
}

func (e *syntaxError) Error() string {
	var buf bytes.Buffer
	e.PrettyPrint(&Sink{&buf}, defaultColorize, defaultIncludeSource)
	return buf.String()
}

type TypeError struct {
	DstType         reflect.Type
	SrcType         reflect.Type
	StructFieldName *string
	Token           *token.Token
}

func (e *TypeError) Error() string {
	if e.StructFieldName != nil {
		return fmt.Sprintf("cannot unmarshal %s into Go struct field %s of type %s", e.SrcType, *e.StructFieldName, e.DstType)
	}
	return fmt.Sprintf("cannot unmarshal %s into Go value of type %s", e.SrcType, e.DstType)
}

func (e *TypeError) PrettyPrint(p Printer, colored, inclSource bool) error {
	return e.FormatError(&FormatErrorPrinter{Printer: p, Colored: colored, InclSource: inclSource})
}

func (e *TypeError) FormatError(p Printer) error {
	var pp printer.Printer

	var colored, inclSource bool
	if fep, ok := p.(*FormatErrorPrinter); ok {
		colored = fep.Colored
		inclSource = fep.InclSource
	}

	pos := fmt.Sprintf("[%d:%d] ", e.Token.Position.Line, e.Token.Position.Column)
	msg := pp.PrintErrorMessage(fmt.Sprintf("%s%s", pos, e.Error()), colored)
	if inclSource {
		msg += "\n" + pp.PrintErrorToken(e.Token, colored)
	}
	p.Print(msg)

	return nil
}
