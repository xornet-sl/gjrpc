package gjrpc

import (
	"context"
	"errors"
	"fmt"
	"log"
	"reflect"
	"runtime"
	"sync"
)

var (
	contextType = reflect.TypeOf((*context.Context)(nil)).Elem()
	handlerType = reflect.TypeOf((*ApiHandler)(nil)).Elem()
	errorType   = reflect.TypeOf((*error)(nil)).Elem()
	// nilValue    = reflect.ValueOf(nil)
)

type registry struct {
	m           sync.RWMutex
	callbacks   map[string]*callback
	muteCrashes bool
}

func (this *registry) registerCallback(name string, fn interface{}) (*callback, error) {
	fnVal := reflect.ValueOf(fn)
	if !fnVal.IsValid() {
		return nil, fmt.Errorf("Unable to register callback. It must be a function")
	}
	c := &callback{name: name, reg: this, fn: fnVal}
	c.makeArgTypes()

	this.m.Lock()
	defer this.m.Unlock()
	if _, ok := this.callbacks[name]; ok {
		return nil, fmt.Errorf("there is already registered callback under the name %s", name)
	}
	if this.callbacks == nil {
		this.callbacks = make(map[string]*callback)
	}
	this.callbacks[name] = c
	return c, nil
}

func (this *registry) dropCallback(name string) {
	this.m.Lock()
	defer this.m.Unlock()
	delete(this.callbacks, name)
}

func (this *registry) getCallback(name string) *callback {
	this.m.RLock()
	defer this.m.RUnlock()
	if cb, ok := this.callbacks[name]; ok {
		return cb
	}
	return nil
}

func (this *registry) call(ctx context.Context, handler *ApiHandler, name string, args ...interface{}) (res interface{}, errRes error) {
	cb := this.getCallback(name)
	if cb == nil {
		return nil, fmt.Errorf("callback %s isn't registered", name)
	}

	refArgs := make([]reflect.Value, 0, len(args))
	for _, arg := range args {
		refArgs = append(refArgs, reflect.ValueOf(arg))
	}

	return cb.call(ctx, handler, refArgs)
}

type callback struct {
	name       string
	reg        *registry
	fn         reflect.Value
	argTypes   []reflect.Type
	hasCtx     bool
	ctxPtr     bool
	hasHandler bool
}

func (this *callback) call(ctx context.Context, handler *ApiHandler, args []reflect.Value) (res interface{}, errRes error) {
	fullArgs := make([]reflect.Value, 0, 1+len(args))
	if this.hasCtx {
		if this.ctxPtr {
			fullArgs = append(fullArgs, reflect.ValueOf(&ctx))
		} else {
			fullArgs = append(fullArgs, reflect.ValueOf(ctx))
		}
	}
	if this.hasHandler {
		fullArgs = append(fullArgs, reflect.ValueOf(handler))
	}
	fullArgs = append(fullArgs, args...)

	// Catch panic while running the callback.
	defer func() {
		if err := recover(); err != nil {
			if !this.reg.muteCrashes {
				const size = 64 << 10
				buf := make([]byte, size)
				buf = buf[:runtime.Stack(buf, false)]
				log.Print("RPC method " + this.name + " crashed: " + fmt.Sprintf("%v\n%s", err, buf))
				// log.Error("RPC method " + name + " crashed: " + fmt.Sprintf("%v\n%s", err, buf))
			}
			errRes = errors.New("method handler crashed")
		}
	}()

	results := this.fn.Call(fullArgs)
	if len(results) == 0 {
		return nil, nil
	}
	errRes = nil
	resInterfaces := make([]interface{}, len(results))
	for idx, res := range results {
		if errRes == nil && isErrorType(res.Type()) {
			errRes, _ = res.Interface().(error)
		}
		resInterfaces[idx] = res.Interface()
	}
	if len(resInterfaces) > 1 {
		return resInterfaces, errRes
	} else {
		return resInterfaces[0], errRes
	}
}

func (this *callback) makeArgTypes() {
	fnType := this.fn.Type()
	firstArg := 0
	if fnType.NumIn() > firstArg && isContextType(fnType.In(firstArg)) {
		this.hasCtx = true
		this.ctxPtr = fnType.In(firstArg).Kind() == reflect.Ptr
		firstArg++
	}
	if fnType.NumIn() > firstArg && isHandlerType(fnType.In(firstArg)) {
		this.hasHandler = true
		firstArg++
	}
	this.argTypes = make([]reflect.Type, fnType.NumIn()-firstArg)
	for i := firstArg; i < fnType.NumIn(); i++ {
		this.argTypes[i-firstArg] = fnType.In(i)
	}
}

func isHandlerType(t reflect.Type) bool {
	return t.Kind() == reflect.Ptr && t.Elem() == handlerType
}

func isContextType(t reflect.Type) bool {
	for t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	return t == contextType
}

func isErrorType(t reflect.Type) bool {
	for t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	return t.Implements(errorType)
}
