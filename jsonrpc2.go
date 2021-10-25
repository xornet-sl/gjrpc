package gjrpc

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
)

const (
	version = "2.0"
)

var (
	jsonNull = json.RawMessage("null")
)

type jsonrpcMessage struct {
	Version string          `json:"jsonrpc,omitempty"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Error   *jsonError      `json:"error,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
}

func (this *jsonrpcMessage) isNotification() bool {
	return this.ID == nil && this.Method != ""
}

func (this *jsonrpcMessage) isCall() bool {
	return this.hasValidID() && this.Method != ""
}

func (this *jsonrpcMessage) isResponse() bool {
	return this.hasValidID() && this.Method == "" && this.Params == nil && (this.Result != nil || this.Error != nil)
}

func (this *jsonrpcMessage) hasValidID() bool {
	return len(this.ID) > 0 && this.ID[0] != '{' && this.ID[0] != '['
}

func (this *jsonrpcMessage) String() string {
	ret, _ := json.Marshal(this)
	return string(ret)
}

func (this *jsonrpcMessage) errorResponse(err error) *jsonrpcMessage {
	resp := errorMessage(err)
	resp.ID = this.ID
	return resp
}

func (this *jsonrpcMessage) response(result interface{}) *jsonrpcMessage {
	enc, err := json.Marshal(result)
	if err != nil {
		return this.errorResponse(err)
	}
	return &jsonrpcMessage{Version: version, ID: this.ID, Result: enc}
}

func requestMessage(id interface{}, method string, args ...interface{}) (*jsonrpcMessage, error) {
	msgId, err := json.Marshal(id)
	if err != nil {
		return nil, err
	}
	msg := &jsonrpcMessage{
		Version: version,
		ID:      msgId,
		Method:  method,
	}
	if args != nil {
		if msg.Params, err = json.Marshal(args); err != nil {
			return nil, err
		}
	}
	return msg, nil
}

func errorMessage(err error) *jsonrpcMessage {
	msg := &jsonrpcMessage{Version: version, ID: jsonNull, Error: &jsonError{
		Code:    defaultErrorCode,
		Message: err.Error(),
	}}
	ec, ok := err.(Error)
	if ok {
		msg.Error.Code = ec.ErrorCode()
	}
	de, ok := err.(DataError)
	if ok {
		msg.Error.Data = de.ErrorData()
	}
	return msg
}

type jsonError struct {
	Code    int         `json:"code"`
	Message string      `json:"message"`
	Data    interface{} `json:"data,omitempty"`
}

func (this *jsonError) Error() string {
	if this.Message == "" {
		return fmt.Sprintf("json-rpc error %d", this.Code)
	}
	return this.Message
}

// isBatch returns true when the first non-whitespace characters is '['
func isBatch(raw json.RawMessage) bool {
	for _, c := range raw {
		// skip insignificant whitespace (http://www.ietf.org/rfc/rfc4627.txt)
		if c == 0x20 || c == 0x09 || c == 0x0a || c == 0x0d {
			continue
		}
		return c == '['
	}
	return false
}

func parseMessage(data []byte) (messages []*jsonrpcMessage, batch bool, err error) {
	var rawmsg json.RawMessage
	messages = []*jsonrpcMessage{{}}
	batch = false
	err = json.Unmarshal(data, &rawmsg)
	if err != nil {
		return
	}
	if !isBatch(rawmsg) {
		err = json.Unmarshal(rawmsg, &messages[0])
		return
	}
	batch = true
	err = json.Unmarshal(rawmsg, &messages)
	return
}

func parsePositionalArguments(rawArgs json.RawMessage, types []reflect.Type) ([]reflect.Value, error) {
	dec := json.NewDecoder(bytes.NewReader(rawArgs))
	var args []reflect.Value
	tok, err := dec.Token()
	switch {
	case err == io.EOF || tok == nil && err == nil:
		// "params" is optional and may be empty. Also allow "params":null even though it's not in the spec.
	case err != nil:
		return nil, err
	case tok == json.Delim('['):
		// Read argument array.
		if args, err = parseArgumentArray(dec, types); err != nil {
			return nil, err
		}
	default:
		return nil, errors.New("non-array args")
	}
	// Set any missing args to nil.
	for i := len(args); i < len(types); i++ {
		if types[i].Kind() != reflect.Ptr {
			return nil, fmt.Errorf("missing value for required argument %d", i)
		}
		args = append(args, reflect.Zero(types[i]))
	}
	return args, nil
}

func parseArgumentArray(dec *json.Decoder, types []reflect.Type) ([]reflect.Value, error) {
	args := make([]reflect.Value, 0, len(types))
	for i := 0; dec.More(); i++ {
		if i >= len(types) {
			return args, fmt.Errorf("too many arguments, want at most %d", len(types))
		}
		argval := reflect.New(types[i])
		if err := dec.Decode(argval.Interface()); err != nil {
			return args, fmt.Errorf("invalid argument %d: %v", i, err)
		}
		if argval.IsNil() && types[i].Kind() != reflect.Ptr {
			return args, fmt.Errorf("missing value for required argument %d", i)
		}
		args = append(args, argval.Elem())
	}
	// Read end of args array.
	_, err := dec.Token()
	return args, err
}
