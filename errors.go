package gjrpc

import (
	"errors"
	"fmt"
)

const (
	defaultErrorCode = -32000
)

var (
	_ Error = new(internalError)
	_ Error = new(methodNotFoundError)
	_ Error = new(parseError)
	_ Error = new(invalidRequestError)
	_ Error = new(invalidMessageError)
	_ Error = new(invalidParamsError)

	RPCError             = errors.New("RPC Error")
	WritingToClosedError = fmt.Errorf("%w (Writing to closed connection)", RPCError)
	NoConnectionError    = fmt.Errorf("%w (No suitable connection)", RPCError)
	WrongConnectionError = fmt.Errorf("%w (Connection doesn't belong to the current RPC instance)", RPCError)
)

type Error interface {
	Error() string
	ErrorCode() int
}

type DataError interface {
	Error() string
	ErrorData() interface{}
}

type internalError struct{ message string }

func (this *internalError) ErrorCode() int { return -32603 }

func (this *internalError) Error() string { return this.message }

type methodNotFoundError struct{ method string }

func (this *methodNotFoundError) ErrorCode() int { return -32601 }

func (this *methodNotFoundError) Error() string {
	return fmt.Sprintf("the method %s does not exist/is not available", this.method)
}

// Invalid JSON was received by the server.
type parseError struct{ message string }

func (this *parseError) ErrorCode() int { return -32700 }

func (this *parseError) Error() string { return this.message }

// received message isn't a valid request
type invalidRequestError struct{ message string }

func (this *invalidRequestError) ErrorCode() int { return -32600 }

func (this *invalidRequestError) Error() string { return this.message }

// received message is invalid
type invalidMessageError struct{ message string }

func (this *invalidMessageError) ErrorCode() int { return -32700 }

func (this *invalidMessageError) Error() string { return this.message }

// unable to decode supplied params, or an invalid number of parameters
type invalidParamsError struct{ message string }

func (this *invalidParamsError) ErrorCode() int { return -32602 }

func (this *invalidParamsError) Error() string { return this.message }
