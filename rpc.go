package gjrpc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"reflect"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

const (
	wsCloseTimeout              = time.Second
	pongTimeout                 = 25 * time.Second
	pingPeriod                  = 10 * time.Second
	defWriteTimeout             = 5 * time.Second
	defGracefullShutdownTimeout = 5 * time.Second
	defReconnectInterval        = 1 * time.Second
)

type resultPlaceholderType struct{}

var (
	resultPlaceholder = &resultPlaceholderType{}
)

type ApiHandler struct {
	Ctx         context.Context
	ContextData interface{}
	Rpc         *Rpc
	Conn        *websocket.Conn
	Interrupt   chan struct{}
	closed      chan struct{}
	writeQueue  chan *writeBuff
}

func (this *ApiHandler) Call(ctx context.Context, result interface{}, method string, args ...interface{}) error {
	if this.Conn == nil || this.Rpc == nil {
		return fmt.Errorf("%w: rpc broken", RPCError)
	}
	if result == nil {
		result = resultPlaceholder
	}
	return this.Rpc.call(ctx, this, result, method, args...)
}

func (this *ApiHandler) Notify(ctx context.Context, method string, args ...interface{}) error {
	if this.Conn == nil || this.Rpc == nil {
		return fmt.Errorf("%w: rpc broken", RPCError)
	}
	return this.Rpc.call(ctx, this, nil, method, args...)
}

type requestJob struct {
	ctx      context.Context
	request  *jsonrpcMessage
	response chan *jsonrpcMessage
}

type writeBuff struct {
	ctx  context.Context
	data []byte
	sent chan error
}

type Rpc struct {
	ctx      context.Context
	Shutdown context.CancelFunc

	maxReconnectRetries int32
	reconnectInterval   time.Duration

	reqId    uint64
	dialer   websocket.Dialer
	upgrader websocket.Upgrader
	reg      registry

	handlers   []*ApiHandler
	handlersMu sync.RWMutex

	jobs       map[uint64]*requestJob
	jobsMu     sync.RWMutex
	writeQueue chan *writeBuff

	onNewHandlerCallback   func(handler *ApiHandler) error
	onCloseHandlerCallback func(handler *ApiHandler, err error)
	onReconnectCallback    func(err error, urlStr string, retryN int32) error
}

// NewRpc creates new RPC instance
func NewRpc(ctx context.Context) *Rpc {
	rpcCtx, rpcCancel := context.WithCancel(ctx)
	return &Rpc{
		ctx:                 rpcCtx,
		Shutdown:            rpcCancel,
		reqId:               0,
		maxReconnectRetries: 0,
		reconnectInterval:   defReconnectInterval,
		handlers:            make([]*ApiHandler, 0),
		jobs:                make(map[uint64]*requestJob),
		writeQueue:          make(chan *writeBuff),
		upgrader: websocket.Upgrader{
			ReadBufferSize:  2048,
			WriteBufferSize: 2048,
		},
	}
}

func (this *Rpc) addHandler(ctx context.Context, conn *websocket.Conn, contextData interface{}) *ApiHandler {
	handler := &ApiHandler{
		Ctx:         ctx,
		ContextData: contextData,
		Interrupt:   make(chan struct{}),
		closed:      make(chan struct{}),
		writeQueue:  make(chan *writeBuff),
		Rpc:         this,
		Conn:        conn,
	}
	this.handlersMu.Lock()
	defer this.handlersMu.Unlock()
	this.handlers = append(this.handlers, handler)
	return handler
}

func (this *Rpc) removeHandler(handler *ApiHandler) {
	this.handlersMu.Lock()
	defer this.handlersMu.Unlock()
	newHandlers := make([]*ApiHandler, 0, len(this.handlers))
	for _, h := range this.handlers {
		if h != handler {
			newHandlers = append(newHandlers, h)
		}
	}
	this.handlers = newHandlers
}

func (this *Rpc) GetHandlersCount() int {
	this.handlersMu.RLock()
	defer this.handlersMu.RUnlock()
	return len(this.handlers)
}

// Call calls remote method with args... arguments and places th result in result arg.
// result can be either a nil or a Ptr
// This function blocks on remote procedure and waits for a result
func (this *Rpc) Call(ctx context.Context, result interface{}, method string, args ...interface{}) error {
	if result == nil {
		result = resultPlaceholder
	}
	return this.call(ctx, nil, result, method, args...)
}

func (this *Rpc) call(ctx context.Context, handler *ApiHandler, result interface{}, method string, args ...interface{}) error {
	if result != nil && reflect.TypeOf(result).Kind() != reflect.Ptr {
		return fmt.Errorf("call result parameter must be pointer or nil interface: %v", result)
	}
	if err := func() error {
		this.handlersMu.RLock()
		defer this.handlersMu.RUnlock()
		if len(this.handlers) == 0 {
			return NoConnectionError
		}
		if handler == nil {
			return nil
		}
		for _, h := range this.handlers {
			if h == handler {
				return nil
			}
		}
		return WrongConnectionError
	}(); err != nil {
		return err
	}
	nextId := this.nextID()
	request, err := requestMessage(nextId, method, args...)
	if err != nil {
		return err
	}
	job := &requestJob{
		ctx:      ctx,
		request:  request,
		response: make(chan *jsonrpcMessage),
	}
	writeBuff := &writeBuff{
		ctx:  ctx,
		sent: make(chan error),
	}
	writeBuff.data, err = json.Marshal(request)
	if err != nil {
		return err
	}
	if result != nil {
		func() {
			this.jobsMu.Lock()
			defer this.jobsMu.Unlock()
			this.jobs[nextId] = job
		}()
		defer func() {
			this.jobsMu.Lock()
			defer this.jobsMu.Unlock()
			delete(this.jobs, nextId)
		}()
	}
	dst := this.writeQueue
	var closed chan struct{}
	if handler != nil {
		dst = handler.writeQueue
		closed = handler.closed
	} else {
		closed = make(chan struct{})
		defer close(closed)
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-closed:
		return WritingToClosedError
	case dst <- writeBuff:
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-closed:
		return WritingToClosedError
	case err = <-writeBuff.sent:
		if err != nil {
			return err
		}
	}
	if result == nil {
		return nil
	}

	var response *jsonrpcMessage
	select {
	case <-ctx.Done():
		return ctx.Err()
	case response = <-job.response:
	}
	if response.Error != nil {
		return errors.New(response.Error.Error())
	}
	if isResultPlaceholder(reflect.TypeOf(result)) {
		return nil
	}
	return json.Unmarshal(response.Result, &result)
}

// Notify calls remote method without waiting for result. This function blocks only for sending time
func (this *Rpc) Notify(ctx context.Context, method string, args ...interface{}) error {
	return this.call(ctx, nil, nil, method, args...)
}

// Broadcast notifies all currently active connections
func (this *Rpc) Broadcast(ctx context.Context, method string, args ...interface{}) error {
	handlers := func() []*ApiHandler {
		this.handlersMu.RLock()
		defer this.handlersMu.RUnlock()
		return this.handlers
	}()
	for _, h := range handlers {
		err := this.call(ctx, h, nil, method, args...)
		if errors.Is(err, WritingToClosedError) {
			continue
		}
		return err
	}
	return nil
}

// RegisterMethod can be used for exporting local method under the name 'name'
func (this *Rpc) RegisterMethod(name string, fn interface{}) error {
	_, err := this.reg.registerCallback(name, fn)
	return err
}

// DropMethod discontinues exporting the method
func (this *Rpc) DropMethod(name string) {
	this.reg.dropCallback(name)
}

func (this *Rpc) SetWebsocketDialer(dialer websocket.Dialer) {
	this.dialer = dialer
}

func (this *Rpc) SetWebsocketUpgrader(upgrader websocket.Upgrader) {
	this.upgrader = upgrader
}

func (this *Rpc) SetMaxReconnectRetries(maxRetries int32) {
	if maxRetries < -1 {
		maxRetries = -1
	}
	atomic.StoreInt32(&this.maxReconnectRetries, maxRetries)
}

func (this *Rpc) SetReconnectInterval(interval time.Duration) {
	atomic.StoreInt64((*int64)(&this.reconnectInterval), int64(interval))
}

// Callbacks

func (this *Rpc) SetOnNewHandlerCallback(callback func(handler *ApiHandler) error) {
	this.onNewHandlerCallback = callback
}

func (this *Rpc) SetOnCloseHandlerCallback(callback func(handler *ApiHandler, err error)) {
	this.onCloseHandlerCallback = callback
}

func (this *Rpc) SetOnReconnectCallback(callback func(err error, urlStr string, retryN int32) error) {
	this.onReconnectCallback = callback
}

func (this *Rpc) defaultOnNewHandlerCallback(handler *ApiHandler) error {
	if this.onNewHandlerCallback != nil {
		return this.onNewHandlerCallback(handler)
	}
	return nil
}

func (this *Rpc) defaultOnCloseHandlerCallback(handler *ApiHandler, err error) {
	if this.onCloseHandlerCallback != nil {
		this.onCloseHandlerCallback(handler, err)
	}
}

func (this *Rpc) defaultOnReconnectCallback(err error, urlStr string, retryN int32) error {
	if this.onReconnectCallback != nil {
		return this.onReconnectCallback(err, urlStr, retryN)
	}
	return nil
}

func (this *Rpc) nextID() uint64 {
	return atomic.AddUint64(&this.reqId, 1)
}

func (this *Rpc) callBatch(handler *ApiHandler, messages []*jsonrpcMessage) []*jsonrpcMessage {
	var mu sync.Mutex
	var wg sync.WaitGroup
	ctx := context.Background()
	if handler != nil {
		ctx = handler.Ctx
	}
	responseMessages := make([]*jsonrpcMessage, 0, len(messages))
	for _, message := range messages {
		cb := this.reg.getCallback(message.Method)
		wg.Add(1)
		go func(cb *callback, message *jsonrpcMessage) {
			var response *jsonrpcMessage
			defer wg.Done()
			defer func() {
				if response != nil {
					mu.Lock()
					defer mu.Unlock()
					responseMessages = append(responseMessages, response)
				}
			}()
			if cb == nil {
				response = message.errorResponse(&methodNotFoundError{method: message.Method})
				return
			}
			args, err := parsePositionalArguments(message.Params, cb.argTypes)
			if err != nil {
				response = message.errorResponse(&invalidParamsError{err.Error()})
				return
			}
			// TODO: select ctx.Done()
			result, err := cb.call(ctx, handler, args)
			if err != nil {
				response = message.errorResponse(err)
				return
			}
			response = message.response(result)
		}(cb, message)
	}
	wg.Wait()
	return responseMessages
}

func (this *Rpc) dispatchInput(handler *ApiHandler, data []byte) {
	writeBuff := &writeBuff{
		ctx: context.TODO(),
	}
	messages, batch, err := parseMessage(data)
	if err != nil {
		writeBuff.data, err = json.Marshal(errorMessage(&parseError{message: fmt.Sprintf("Parse error: %v", err)}))
		if err != nil {
			fmt.Println("Unexpected error on crafting parse error response: ", err)
		} else if len(writeBuff.data) > 0 {
			handler.writeQueue <- writeBuff
		}
		return
	}
	callMessages := make([]*jsonrpcMessage, 0, len(messages))
	for _, message := range messages {
		if message.isResponse() {
			var id uint64
			err = json.Unmarshal(message.ID, &id)
			if err != nil {
				continue
			}
			job := func() *requestJob {
				this.jobsMu.Lock()
				defer this.jobsMu.Unlock()
				if j, ok := this.jobs[id]; ok {
					delete(this.jobs, id)
					return j
				}
				return nil
			}()
			select {
			case <-job.ctx.Done():
				continue
			case job.response <- message:
			}
		} else if message.isCall() {
			callMessages = append(callMessages, message)
		}
	}
	responseMessages := this.callBatch(handler, callMessages)
	if batch {
		writeBuff.data, err = json.Marshal(responseMessages)
		if err != nil {
			writeBuff.data, err = json.Marshal(errorMessage(&internalError{message: "Unable to encode batch result"}))
		}
	} else if len(responseMessages) > 0 {
		writeBuff.data, err = json.Marshal(responseMessages[0])
		if err != nil {
			writeBuff.data, err = json.Marshal(responseMessages[0].errorResponse(&internalError{message: "Unable to encode result"}))
		}
	}
	if err != nil || len(writeBuff.data) == 0 {
		return
	}
	handler.writeQueue <- writeBuff
}

func (this *Rpc) serveApi(ctx context.Context, conn *websocket.Conn, contextData interface{}) (serveErr error) {
	handler := this.addHandler(ctx, conn, contextData)
	defer this.removeHandler(handler)
	if serveErr = this.defaultOnNewHandlerCallback(handler); serveErr != nil {
		return
	}
	readerError := make(chan error, 1)
	defer close(readerError)
	defer func() { this.defaultOnCloseHandlerCallback(handler, serveErr) }()

	go func() {
		defer close(handler.closed)
		_ = conn.SetReadDeadline(time.Now().Add(pongTimeout))
		conn.SetPongHandler(func(data string) error { return conn.SetReadDeadline(time.Now().Add(pongTimeout)) })
		for {
			messageType, message, err := conn.ReadMessage()
			if websocket.IsCloseError(err, websocket.CloseNormalClosure) {
				readerError <- nil
				return
			} else if err != nil {
				readerError <- err
				return
			}
			fmt.Printf("recv type %d: %s\n", messageType, message)
			go this.dispatchInput(handler, message)
		}
	}()

	pingTicker := time.NewTicker(pingPeriod)
	var writeBuff *writeBuff
	getWriteDeadline := func(ctx context.Context) time.Time {
		deadline, ok := ctx.Deadline()
		if !ok {
			deadline = time.Now().Add(defWriteTimeout)
		}
		return deadline
	}
	for {
		writeBuff = nil
		select {
		case <-pingTicker.C:
			if serveErr = conn.WriteControl(websocket.PingMessage, []byte{}, getWriteDeadline(ctx)); serveErr != nil {
				return
			}
		case <-ctx.Done():
			conn.Close()
			serveErr = ctx.Err()
			return
		case <-this.ctx.Done(): // rpc context cancelled
			conn.Close()
			serveErr = this.ctx.Err()
			return
		case <-handler.closed:
			serveErr = <-readerError
			return
		case <-handler.Interrupt:
			err := conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
			if err != nil {
				serveErr = fmt.Errorf("Connection interrupted and there was an error: %w", err)
				return
			}
			select {
			case <-handler.closed:
				err = <-readerError
			case <-time.After(wsCloseTimeout):
				err = errors.New("Timeout while closing connection")
			}
			if err != nil {
				serveErr = fmt.Errorf("Connection interrupted and there was an error: %w", err)
			}
			serveErr = errors.New("Connection interrupted")
			return
		case writeBuff = <-handler.writeQueue:
		case writeBuff = <-this.writeQueue:
		}

		if writeBuff != nil {
			_ = conn.SetWriteDeadline(getWriteDeadline(writeBuff.ctx))
			err := conn.WriteMessage(websocket.TextMessage, writeBuff.data)
			if writeBuff.sent != nil {
				writeBuff.sent <- err
			}
		}
	}
}

func (this *Rpc) DialAndServe(urlStr string, requestHeader http.Header, contextData interface{}) error {
	var nRetries int32 = 0

	for {
		err := func() error {
			conn, resp, err := this.dialer.DialContext(this.ctx, urlStr, requestHeader)
			if err != nil {
				if resp != nil && resp.StatusCode == http.StatusUnauthorized {
					return fmt.Errorf("%w: Unauthorized", err)
				}
				return err
			}
			nRetries = 0
			defer conn.Close()
			return this.serveApi(resp.Request.Context(), conn, contextData)
		}()
		if nRetries < math.MaxInt32 {
			nRetries++
		}
		maxRetries := atomic.LoadInt32(&this.maxReconnectRetries)
		if maxRetries == 0 || maxRetries > 0 && nRetries >= maxRetries {
			return err
		}
		select {
		case <-this.ctx.Done():
			return this.ctx.Err()
		default:
		}
		if err = this.defaultOnReconnectCallback(err, urlStr, nRetries); err != nil {
			return err
		}
		time.Sleep(time.Duration(atomic.LoadInt64((*int64)(&this.reconnectInterval))))
	}
}

func (this *Rpc) GetHTTPHandler(contextDataFn func() interface{}) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		conn, err := this.upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		var contextData interface{}
		if contextDataFn != nil {
			contextData = contextDataFn()
		}
		_ = this.serveApi(r.Context(), conn, contextData)
	}
}

func (this *Rpc) ListenAndServe(addr string, contextDataFn func() interface{}) error {
	srv := &http.Server{Addr: addr, Handler: http.HandlerFunc(this.GetHTTPHandler(contextDataFn))}
	go func() {
		<-this.ctx.Done()
		deadline, cancel := context.WithDeadline(context.Background(), time.Now().Add(defGracefullShutdownTimeout))
		defer cancel()
		if err := srv.Shutdown(deadline); err != nil {
			_ = srv.Close()
		}
	}()
	err := srv.ListenAndServe()
	if err == http.ErrServerClosed {
		return nil
	}
	return err
}

func isResultPlaceholder(t reflect.Type) bool {
	return t == reflect.TypeOf(resultPlaceholder)
}
