package gjrpc_test

import (
	"context"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/xornet-sl/gjrpc"
)

func TestDialerInterrupt(t *testing.T) {
	closingTest(false, t)
}

func TestServerInterrupt(t *testing.T) {
	closingTest(true, t)
}

func closingTest(colseServer bool, t *testing.T) {
	var wg, closingWg sync.WaitGroup
	wg.Add(2)
	closingWg.Add(2)
	exitNow := make(chan int, 10)
	serverReady := make(chan struct{})

	closingNewHandler := func(handler *gjrpc.ApiHandler) error {
		go func() {
			closingWg.Done()
			closingWg.Wait()
			time.Sleep(100 * time.Millisecond)
			close(handler.Interrupt)
		}()
		return nil
	}

	nonClosingNewHandler := func(handler *gjrpc.ApiHandler) error {
		go func() {
			closingWg.Done()
		}()
		return nil
	}

	go func() {
		// Client
		defer wg.Done()
		defer func() { exitNow <- 1 }()
		rpc := gjrpc.NewRpc(context.Background())
		if colseServer {
			rpc.SetOnNewHandlerCallback(nonClosingNewHandler)
		} else {
			rpc.SetOnNewHandlerCallback(closingNewHandler)
		}
		<-serverReady
		time.Sleep(50 * time.Millisecond)
		select {
		case <-exitNow:
			return
		default:
		}
		err := rpc.DialAndServe("ws://localhost:4545/", nil, nil)
		if err != nil && err.Error() != "Connection interrupted" {
			t.Errorf("Dial error: %v", err)
			return
		}
	}()

	go func() {
		// Server
		defer wg.Done()
		defer func() { exitNow <- 1 }()
		rpc := gjrpc.NewRpc(context.Background())
		srv := &http.Server{Addr: ":4545", Handler: http.HandlerFunc(rpc.GetHTTPHandler(nil))}
		if !colseServer {
			rpc.SetOnNewHandlerCallback(nonClosingNewHandler)
		} else {
			rpc.SetOnNewHandlerCallback(closingNewHandler)
		}
		rpc.SetOnCloseHandlerCallback(func(handler *gjrpc.ApiHandler, err error) {
			_ = srv.Close()
		})
		// http.HandleFunc("/", rpc.GetHTTPHandler())
		close(serverReady)
		if err := srv.ListenAndServe(); err != http.ErrServerClosed {
			t.Errorf("http.ListenAndServe error: %s", err)
		}
	}()

	wg.Wait()
}
