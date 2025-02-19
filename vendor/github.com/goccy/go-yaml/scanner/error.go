package scanner

import "github.com/goccy/go-yaml/token"

type InvalidTokenError struct {
	Message string
	Token   *token.Token
}

func (e *InvalidTokenError) Error() string {
	return e.Message
}

func ErrInvalidToken(msg string, tk *token.Token) *InvalidTokenError {
	return &InvalidTokenError{
		Message: msg,
		Token:   tk,
	}
}
